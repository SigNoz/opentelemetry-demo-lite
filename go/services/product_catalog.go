package services

import (
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"

	"github.com/XSAM/otelsql"
	_ "github.com/mattn/go-sqlite3"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	productLogger  *slog.Logger
	productMeter   metric.Meter
	productCounter metric.Int64Counter
)

// Mock product data
type Product struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Price       float64  `json:"price"`
	Categories  []string `json:"categories"`
}

var products = []Product{
	{ID: "OLJCESPC7Z", Name: "Sunglasses", Description: "High quality sunglasses", Price: 19.99, Categories: []string{"accessories"}},
	{ID: "66VCHSJNUP", Name: "Tank Top", Description: "Comfortable tank top", Price: 18.99, Categories: []string{"clothing"}},
	{ID: "1YMWWN1N4O", Name: "Watch", Description: "Classic wristwatch", Price: 109.99, Categories: []string{"accessories"}},
	{ID: "L9ECAV7KIM", Name: "Loafers", Description: "Leather loafers", Price: 89.99, Categories: []string{"footwear"}},
	{ID: "2ZYFJ3GM2N", Name: "Hairdryer", Description: "Professional hairdryer", Price: 24.99, Categories: []string{"beauty"}},
	{ID: "0PUK6V6EV0", Name: "Candle Holder", Description: "Decorative candle holder", Price: 15.99, Categories: []string{"home"}},
	{ID: "LS4PSXUNUM", Name: "Salt Shaker", Description: "Ceramic salt shaker", Price: 9.99, Categories: []string{"home"}},
	{ID: "9SIQT8TOJO", Name: "Bamboo Glass Jar", Description: "Eco-friendly glass jar", Price: 14.99, Categories: []string{"home"}},
	{ID: "6E92ZMYYFZ", Name: "Mug", Description: "Ceramic coffee mug", Price: 12.99, Categories: []string{"home"}},
}

var sqliteDB *sql.DB

func initSQLite() {
	// Open with OTel instrumentation
	db, err := otelsql.Open("sqlite3", "file::memory:?cache=shared",
		otelsql.WithAttributes(
			attribute.String("db.system", "sqlite"),
			attribute.String("db.name", "products"),
		),
	)
	if err != nil {
		log.Printf("Failed to open SQLite: %v", err)
		return
	}

	// Register DB stats metrics
	if err := otelsql.RegisterDBStatsMetrics(db,
		otelsql.WithAttributes(attribute.String("db.system", "sqlite")),
	); err != nil {
		log.Printf("Failed to register SQLite metrics: %v", err)
	}

	sqliteDB = db

	// Create and seed products table
	_, err = sqliteDB.Exec(`CREATE TABLE IF NOT EXISTS products (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		price REAL,
		categories TEXT
	)`)
	if err != nil {
		log.Printf("Failed to create products table: %v", err)
		return
	}

	// Seed from existing products slice
	for _, p := range products {
		_, err := sqliteDB.Exec(`INSERT OR REPLACE INTO products VALUES (?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Description, p.Price, strings.Join(p.Categories, ","))
		if err != nil {
			log.Printf("Failed to insert product %s: %v", p.ID, err)
		}
	}
	log.Printf("SQLite initialized with %d products", len(products))
}

func initProductMetrics() {
	productMeter = otel.Meter("product-catalog")
	var err error

	productCounter, err = productMeter.Int64Counter("app.products.requests",
		metric.WithDescription("Number of product catalog requests"),
		metric.WithUnit("{requests}"))
	if err != nil {
		panic(err)
	}
}

func RunProductCatalogService(tp trace.TracerProvider, lp otellog.LoggerProvider) {
	productLogger = otelslog.NewLogger("product-catalog", otelslog.WithLoggerProvider(lp))
	initProductMetrics()
	initSQLite()

	listHandler := otelhttp.NewHandler(
		http.HandlerFunc(listProductsHandler),
		"ListProducts",
		otelhttp.WithTracerProvider(tp),
	)

	getHandler := otelhttp.NewHandler(
		http.HandlerFunc(getProductHandler),
		"GetProduct",
		otelhttp.WithTracerProvider(tp),
	)

	searchHandler := otelhttp.NewHandler(
		http.HandlerFunc(searchProductsHandler),
		"SearchProducts",
		otelhttp.WithTracerProvider(tp),
	)

	mux := http.NewServeMux()
	mux.Handle("/products", listHandler)
	mux.Handle("/products/", getHandler) // /products/{id}
	mux.Handle("/products/search", searchHandler)

	port := ":8085"
	productLogger.Info("Product Catalog Service starting", "port", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		productLogger.Error("Product Catalog Service failed", "error", err)
	}
}

func listProductsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	span.SetAttributes(
		attribute.Int("app.products.count", len(products)),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "oteldemo.ProductCatalogService"),
		attribute.String("rpc.method", "ListProducts"),
	)

	productCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", "ListProducts"),
	))

	productLogger.InfoContext(ctx, "ListProducts", "count", len(products))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"products": %d}`, len(products))
}

func getProductHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	// Extract product ID from path
	path := r.URL.Path
	id := strings.TrimPrefix(path, "/products/")

	span.SetAttributes(
		attribute.String("app.product.id", id),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "oteldemo.ProductCatalogService"),
		attribute.String("rpc.method", "GetProduct"),
	)

	// Query SQLite (auto-instrumented - creates db span)
	var found Product
	err := sqliteDB.QueryRowContext(ctx,
		`SELECT id, name, description, price FROM products WHERE id = ?`, id).
		Scan(&found.ID, &found.Name, &found.Description, &found.Price)

	if err == sql.ErrNoRows {
		span.SetAttributes(attribute.Bool("product.found", false))
		productCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("method", "GetProduct"),
			attribute.String("status", "not_found"),
		))
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	}
	if err != nil {
		span.RecordError(err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	span.SetAttributes(
		attribute.String("app.product.name", found.Name),
		attribute.Bool("product.found", true),
	)

	productCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", "GetProduct"),
		attribute.String("status", "found"),
	))

	productLogger.InfoContext(ctx, "GetProduct",
		"product_id", id,
		"product_name", found.Name,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"id": "%s", "name": "%s", "price": %.2f}`, found.ID, found.Name, found.Price)
}

func searchProductsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)

	query := r.URL.Query().Get("q")
	if query == "" {
		query = "sunglasses"
	}

	// Mock search
	var results []Product
	queryLower := strings.ToLower(query)
	for _, p := range products {
		if strings.Contains(strings.ToLower(p.Name), queryLower) ||
			strings.Contains(strings.ToLower(p.Description), queryLower) {
			results = append(results, p)
		}
	}

	span.SetAttributes(
		attribute.String("search.query", query),
		attribute.Int("app.products_search.count", len(results)),
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "oteldemo.ProductCatalogService"),
		attribute.String("rpc.method", "SearchProducts"),
	)

	productCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", "SearchProducts"),
	))

	productLogger.InfoContext(ctx, "SearchProducts",
		"query", query,
		"results", len(results),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"query": "%s", "results": %d}`, query, len(results))
}

// GetRandomProduct returns a random product for other services to use
func GetRandomProduct() Product {
	return products[rand.Intn(len(products))]
}

// GetProductID returns a random product ID
func GetProductID() string {
	return products[rand.Intn(len(products))].ID
}
