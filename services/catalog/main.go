package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker"
)

var (
	db                *pgxpool.Pool
	rdb               *redis.Client
	catalogBreaker    *gobreaker.CircuitBreaker
	httpClient        *http.Client
	catalogServiceURL string
)

type CreateOrderRequest struct {
	RestaurantID string        `json:"restaurant_id"`
	Items        []OrderItemReq `json:"items"`
	DeliveryAddr *DeliveryAddr `json:"delivery_address,omitempty"`
}

type OrderItemReq struct {
	MenuItemID string `json:"menu_item_id"`
	Quantity   int    `json:"quantity"`
}

type DeliveryAddr struct {
	City   string  `json:"city"`
	Street string  `json:"street"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

type OrderResponse struct {
	OrderID     string `json:"order_id"`
	Status      string `json:"status"`
	TotalAmount int    `json:"total_amount"`
	CreatedAt   string `json:"created_at"`
}

type OrderStatusResponse struct {
	OrderID string          `json:"order_id"`
	Status  string          `json:"status"`
	Items   []OrderItemResp `json:"items"`
}

type OrderItemResp struct {
	MenuItemID string `json:"menu_item_id"`
	Name       string `json:"name"`
	Quantity   int    `json:"quantity"`
	Price      int    `json:"price"`
}

type MenuItem struct {
	ID          string `json:"menu_item_id"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	IsAvailable bool   `json:"is_available"`
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
	config.MaxConns = 15
	config.MinConns = 5
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
		PoolSize:     10,
		MinIdleConns: 5,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Println("Warning: Redis not available")
	}

	catalogServiceURL = os.Getenv("CATALOG_SERVICE_URL")
	if catalogServiceURL == "" {
		catalogServiceURL = "http://localhost:8081"
	}

	httpClient = &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	catalogBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "catalog-service",
		MaxRequests: 5,
		Interval:    10 * time.Second,
		Timeout:     5 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= 10 && failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			log.Printf("Circuit Breaker '%s' changed from %s to %s", name, from, to)
		},
	})

	go startOutboxWorker()

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
			"service":   "order",
			"timestamp": time.Now().UTC(),
		})
	})

	r.Post("/api/v1/orders", createOrder)
	r.Get("/api/v1/orders/{orderID}", getOrderStatus)

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8082"
	}

	log.Printf("Order service starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func validateMenuItems(ctx context.Context, restaurantID string, items []OrderItemReq) (map[string]MenuItem, error) {
	body, err := catalogBreaker.Execute(func() (interface{}, error) {
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(reqCtx, "GET",
			catalogServiceURL+"/api/v1/restaurants/"+restaurantID+"/menu", nil)
		if err != nil {
			return nil, err
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("catalog returned %d", resp.StatusCode)
		}

		return io.ReadAll(resp.Body)
	})

	if err != nil {
		return nil, fmt.Errorf("catalog service unavailable: %w", err)
	}

	var menu struct {
		Items []MenuItem `json:"items"`
	}
	if err := json.Unmarshal(body.([]byte), &menu); err != nil {
		return nil, fmt.Errorf("failed to parse menu: %w", err)
	}

	availableItems := make(map[string]MenuItem)
	for _, item := range menu.Items {
		availableItems[item.ID] = item
	}

	return availableItems, nil
}

func createOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "invalid_request",
			"message": "Невалидный JSON в теле запроса",
		})
		return
	}

	if req.RestaurantID == "" || len(req.Items) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "validation_error",
			"message": "restaurant_id и items обязательны",
		})
		return
	}

	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "missing_idempotency_key",
			"message": "Заголовок Idempotency-Key обязателен",
		})
		return
	}

	var existingOrder OrderResponse
	err := db.QueryRow(ctx,
		`SELECT order_id, status, total_amount, created_at 
		 FROM orders WHERE idempotency_key = $1`,
		idemKey,
	).Scan(&existingOrder.OrderID, &existingOrder.Status, &existingOrder.TotalAmount, &existingOrder.CreatedAt)

	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Idempotency-Replay", "true")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(existingOrder)
		return
	}

	availableItems, err := validateMenuItems(ctx, req.RestaurantID, req.Items)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "catalog_unavailable",
			"message": "Сервис каталога временно недоступен: " + err.Error(),
		})
		return
	}

	totalAmount := 0
	for _, item := range req.Items {
		menuItem, exists := availableItems[item.MenuItemID]
		if !exists || !menuItem.IsAvailable {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error":   "item_unavailable",
				"message": fmt.Sprintf("Блюдо %s недоступно", item.MenuItemID),
			})
			return
		}
		totalAmount += menuItem.Price * item.Quantity
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		log.Printf("Error beginning transaction: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	deliveryAddr := `{"city":"","street":""}`
	if req.DeliveryAddr != nil {
		deliveryAddr = fmt.Sprintf(`{"city":"%s","street":"%s"}`,
			req.DeliveryAddr.City, req.DeliveryAddr.Street)
	}

	var orderID string
	var createdAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO orders (restaurant_id, user_id, status, total_amount, idempotency_key, delivery_address)
		 VALUES ($1, $2, 'created', $3, $4, $5)
		 RETURNING order_id, created_at`,
		req.RestaurantID,
		"00000000-0000-0000-0000-000000000001",
		totalAmount,
		idemKey,
		deliveryAddr,
	).Scan(&orderID, &createdAt)

	if err != nil {
		log.Printf("Error inserting order: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	for _, item := range req.Items {
		menuItem := availableItems[item.MenuItemID]
		_, err = tx.Exec(ctx,
			`INSERT INTO order_items (order_id, menu_item_id, quantity, price_snapshot, name_snapshot)
			 VALUES ($1, $2, $3, $4, $5)`,
			orderID, item.MenuItemID, item.Quantity, menuItem.Price, menuItem.Name,
		)
		if err != nil {
			log.Printf("Error inserting order item: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	outboxPayload, _ := json.Marshal(map[string]interface{}{
		"order_id":      orderID,
		"restaurant_id": req.RestaurantID,
		"total_amount":  totalAmount,
		"items":         req.Items,
	})

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, event_type, payload)
		 VALUES ($1, 'OrderCreated', $2)`,
		orderID, outboxPayload,
	)
	if err != nil {
		log.Printf("Error inserting outbox event: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("Error committing transaction: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	order := OrderResponse{
		OrderID:     orderID,
		Status:      "created",
		TotalAmount: totalAmount,
		CreatedAt:   createdAt.Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(order)
}

func getOrderStatus(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "orderID")
	ctx := r.Context()

	var order OrderStatusResponse
	err := db.QueryRow(ctx,
		`SELECT order_id, status FROM orders WHERE order_id = $1`,
		orderID,
	).Scan(&order.OrderID, &order.Status)

	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "order_not_found",
			"message": "Заказ не найден",
		})
		return
	}

	rows, err := db.Query(ctx,
		`SELECT menu_item_id, name_snapshot, quantity, price_snapshot
		 FROM order_items WHERE order_id = $1`,
		orderID,
	)
	if err == nil {
		defer rows.Close()
		order.Items = make([]OrderItemResp, 0)
		for rows.Next() {
			var item OrderItemResp
			if err := rows.Scan(&item.MenuItemID, &item.Name, &item.Quantity, &item.Price); err != nil {
				continue
			}
			order.Items = append(order.Items, item)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(order)
}

func startOutboxWorker() {
	log.Println("Starting outbox worker")
	time.Sleep(5 * time.Second)

	for {
		time.Sleep(1 * time.Second)

		ctx := context.Background()

		rows, err := db.Query(ctx,
			`SELECT event_id, event_type, payload 
			 FROM outbox_events 
			 WHERE published = false 
			 ORDER BY created_at ASC 
			 LIMIT 50`,
		)
		if err != nil {
			log.Printf("Outbox worker error: %v", err)
			continue
		}

		count := 0
		for rows.Next() {
			var eventID, eventType string
			var payload []byte

			if err := rows.Scan(&eventID, &eventType, &payload); err != nil {
				continue
			}

			var prettyJSON bytes.Buffer
			json.Indent(&prettyJSON, payload, "", "  ")

			_, err = db.Exec(ctx,
				`UPDATE outbox_events SET published = true WHERE event_id = $1`,
				eventID,
			)
			if err != nil {
				continue
			}
			count++
		}
		rows.Close()

		if count > 0 {
			log.Printf("Published %d outbox events", count)
		}
	}
}