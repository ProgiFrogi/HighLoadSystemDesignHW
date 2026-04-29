package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	db  *pgxpool.Pool
	rdb *redis.Client
)

// Restaurant представляет ресторан в ответе API
type Restaurant struct {
	ID        string  `json:"restaurant_id"`
	Name      string  `json:"name"`
	Rating    float64 `json:"rating"`
	Cuisine   string  `json:"cuisine"`
	Distance  float64 `json:"distance_meters,omitempty"`
	IsOpen    bool    `json:"is_open"`
}

// MenuItem представляет блюдо в меню
type MenuItem struct {
	ID          string `json:"menu_item_id"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	Category    string `json:"category"`
	IsAvailable bool   `json:"is_available"`
}

// MenuResponse ответ с меню ресторана
type MenuResponse struct {
	RestaurantID string     `json:"restaurant_id"`
	Name         string     `json:"name"`
	Items        []MenuItem `json:"items"`
}

// SearchResponse ответ поиска ресторанов
type SearchResponse struct {
	Items  []Restaurant `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func main() {
	// Инициализация подключений
	ctx := context.Background()

	// PostgreSQL
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://fooduser:foodpass@localhost:5432/fooddelivery?sslmode=disable"
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatal("Unable to parse database URL:", err)
	}
	config.MaxConns = 20
	config.MinConns = 5
	config.MaxConnLifetime = 30 * time.Minute

	db, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		log.Fatal("Unable to create connection pool:", err)
	}
	defer db.Close()

	// Проверяем подключение
	if err := db.Ping(ctx); err != nil {
		log.Fatal("Unable to ping database:", err)
	}
	log.Println("Connected to PostgreSQL")

	// Redis
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	rdb = redis.NewClient(&redis.Options{
		Addr:         redisURL,
		Password:     "",
		DB:           0,
		PoolSize:     20,
		MinIdleConns: 5,
		MaxRetries:   3,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatal("Unable to connect to Redis:", err)
	}
	log.Println("Connected to Redis")

	// Настраиваем роутер
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(5 * time.Second))

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"service":   "catalog",
			"timestamp": time.Now().UTC(),
		})
	})

	// API endpoints
	r.Get("/api/v1/restaurants/{restaurantID}/menu", getMenu)
	r.Get("/api/v1/restaurants/search", searchRestaurants)

	// Запуск сервера
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8081"
	}

	log.Printf("Catalog service starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

// getMenu возвращает меню ресторана с кешированием в Redis (Cache-Aside паттерн)
func getMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID := chi.URLParam(r, "restaurantID")
	ctx := r.Context()

	// 1. Пытаемся получить из Redis (Cache-Aside: сначала кеш)
	cacheKey := fmt.Sprintf("menu:%s", restaurantID)
	cached, err := rdb.Get(ctx, cacheKey).Result()
	if err == nil {
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cached))
		return
	}

	// 2. Промах кеша — идём в PostgreSQL
	var menu MenuResponse

	// Получаем информацию о ресторане
	err = db.QueryRow(ctx,
		`SELECT restaurant_id, name FROM restaurants WHERE restaurant_id = $1 AND is_open = true`,
		restaurantID,
	).Scan(&menu.RestaurantID, &menu.Name)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "restaurant_not_found",
			"message": "Ресторан не найден или временно закрыт",
		})
		return
	}

	// Получаем блюда
	rows, err := db.Query(ctx,
		`SELECT menu_item_id, name, price, category, is_available 
		 FROM menu_items 
		 WHERE restaurant_id = $1 AND is_available = true
		 ORDER BY category, name`,
		restaurantID,
	)
	if err != nil {
		log.Printf("Error querying menu: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	menu.Items = make([]MenuItem, 0)
	for rows.Next() {
		var item MenuItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Price, &item.Category, &item.IsAvailable); err != nil {
			log.Printf("Error scanning menu item: %v", err)
			continue
		}
		menu.Items = append(menu.Items, item)
	}

	// 3. Сохраняем в Redis на 5 минут (TTL)
	data, _ := json.Marshal(menu)
	rdb.Set(ctx, cacheKey, data, 5*time.Minute)

	w.Header().Set("X-Cache", "MISS")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(menu)
}

// searchRestaurants ищет рестораны с пагинацией
func searchRestaurants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Парсим параметры запроса
	cuisine := r.URL.Query().Get("cuisine")
	query := r.URL.Query().Get("q")
	latStr := r.URL.Query().Get("lat")
	lonStr := r.URL.Query().Get("lon")

	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 20
	offset := 0

	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
		limit = l
	}
	if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
		offset = o
	}

	// Строим SQL-запрос динамически (только для PoC, используем prepared statements)
	whereClause := "WHERE r.is_open = true"
	args := make([]interface{}, 0)
	argIdx := 1

	if cuisine != "" {
		whereClause += fmt.Sprintf(" AND r.cuisine = $%d", argIdx)
		args = append(args, cuisine)
		argIdx++
	}

	if query != "" {
		whereClause += fmt.Sprintf(" AND (r.name ILIKE $%d OR EXISTS (SELECT 1 FROM menu_items mi WHERE mi.restaurant_id = r.restaurant_id AND mi.name ILIKE $%d))", argIdx, argIdx)
		args = append(args, "%"+query+"%")
		argIdx++
	}

	// Базовый запрос
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM restaurants r %s", whereClause)
	var total int
	err := db.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		log.Printf("Error counting restaurants: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Основной запрос с сортировкой по расстоянию если есть координаты
	var selectQuery string
	if latStr != "" && lonStr != "" {
		lat, err1 := strconv.ParseFloat(latStr, 64)
		lon, err2 := strconv.ParseFloat(lonStr, 64)
		if err1 == nil && err2 == nil {
			selectQuery = fmt.Sprintf(
				`SELECT r.restaurant_id, r.name, r.rating, r.cuisine, r.is_open,
				 ST_Distance(r.location, ST_MakePoint($%d, $%d)::geography) as distance
				 FROM restaurants r %s
				 ORDER BY distance ASC
				 LIMIT $%d OFFSET $%d`,
				argIdx, argIdx+1, whereClause, argIdx+2, argIdx+3,
			)
			args = append(args, lon, lat, limit, offset)
		}
	}

	if selectQuery == "" {
		selectQuery = fmt.Sprintf(
			`SELECT r.restaurant_id, r.name, r.rating, r.cuisine, r.is_open, 0 as distance
			 FROM restaurants r %s
			 ORDER BY r.rating DESC
			 LIMIT $%d OFFSET $%d`,
			whereClause, argIdx, argIdx+1,
		)
		args = append(args, limit, offset)
	}

	rows, err := db.Query(ctx, selectQuery, args...)
	if err != nil {
		log.Printf("Error searching restaurants: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]Restaurant, 0)
	for rows.Next() {
		var rest Restaurant
		if err := rows.Scan(&rest.ID, &rest.Name, &rest.Rating, &rest.Cuisine, &rest.IsOpen, &rest.Distance); err != nil {
			log.Printf("Error scanning restaurant: %v", err)
			continue
		}
		items = append(items, rest)
	}

	response := SearchResponse{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}