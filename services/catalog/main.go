package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
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

type Restaurant struct {
	ID       string  `json:"restaurant_id"`
	Name     string  `json:"name"`
	Rating   float64 `json:"rating"`
	Cuisine  string  `json:"cuisine"`
	Distance float64 `json:"distance_meters,omitempty"`
	IsOpen   bool    `json:"is_open"`
}

type MenuItem struct {
	ID          string `json:"menu_item_id"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	Category    string `json:"category"`
	IsAvailable bool   `json:"is_available"`
}

type MenuResponse struct {
	RestaurantID string     `json:"restaurant_id"`
	Name         string     `json:"name"`
	Items        []MenuItem `json:"items"`
}

type SearchResponse struct {
	Items  []Restaurant `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func main() {
	ctx := context.Background()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://fooduser:foodpass@localhost:5432/fooddelivery?sslmode=disable"
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatal("Unable to parse database URL:", err)
	}
	config.MaxConns = 2
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute

	db, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		log.Fatal("Unable to create connection pool:", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatal("Unable to ping database:", err)
	}
	log.Println("Connected to PostgreSQL")

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	rdb = redis.NewClient(&redis.Options{
		Addr:         redisURL,
		Password:     "",
		DB:           0,
		PoolSize:     2,
		MinIdleConns: 1,
		MaxRetries:   1,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Println("Warning: Redis not available, continuing without cache")
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(5 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"service":   "catalog",
			"timestamp": time.Now().UTC(),
		})
	})

	r.Get("/api/v1/restaurants/{restaurantID}/menu", getMenu)
	r.Get("/api/v1/restaurants/search", searchRestaurants)

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8081"
	}

	log.Printf("Catalog service starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func getMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID := chi.URLParam(r, "restaurantID")
	ctx := r.Context()

	// Iter0: БЕЗ КЕША — сразу в БД
	var menu MenuResponse
	err := db.QueryRow(ctx,
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
			continue
		}
		menu.Items = append(menu.Items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(menu)
}

func searchRestaurants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	baseQuery := `FROM restaurants r WHERE r.is_open = true`
	args := make([]interface{}, 0)
	argIdx := 1

	if cuisine != "" {
		baseQuery += fmt.Sprintf(" AND r.cuisine = $%d", argIdx)
		args = append(args, cuisine)
		argIdx++
	}

	if query != "" {
		baseQuery += fmt.Sprintf(" AND (r.name ILIKE $%d OR EXISTS (SELECT 1 FROM menu_items mi WHERE mi.restaurant_id = r.restaurant_id AND mi.name ILIKE $%d))", argIdx, argIdx)
		args = append(args, "%"+query+"%")
		argIdx++
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) %s", baseQuery)
	err := db.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		log.Printf("Error counting restaurants: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	var selectQuery string
	if latStr != "" && lonStr != "" {
		lat, err1 := strconv.ParseFloat(latStr, 64)
		lon, err2 := strconv.ParseFloat(lonStr, 64)
		if err1 == nil && err2 == nil {
			selectQuery = fmt.Sprintf(
				`SELECT r.restaurant_id, r.name, r.rating, r.cuisine, r.is_open,
				 (6371000 * acos(cos(radians($%d)) * cos(radians(r.lat)) * cos(radians(r.lon) - radians($%d)) + sin(radians($%d)) * sin(radians(r.lat)))) as distance
				 %s
				 ORDER BY distance ASC
				 LIMIT $%d OFFSET $%d`,
				argIdx, argIdx+1, argIdx, baseQuery, argIdx+2, argIdx+3,
			)
			args = append(args, lat, lon, limit, offset)
		}
	}

	if selectQuery == "" {
		selectQuery = fmt.Sprintf(
			`SELECT r.restaurant_id, r.name, r.rating, r.cuisine, r.is_open, 0 as distance
			 %s
			 ORDER BY r.rating DESC
			 LIMIT $%d OFFSET $%d`,
			baseQuery, argIdx, argIdx+1,
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
		var distance float64
		if err := rows.Scan(&rest.ID, &rest.Name, &rest.Rating, &rest.Cuisine, &rest.IsOpen, &distance); err != nil {
			log.Printf("Error scanning: %v", err)
			continue
		}
		rest.Distance = math.Round(distance*100) / 100
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