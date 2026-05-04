package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

var db *sql.DB

type Cat struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Weight struct {
	ID         int64   `json:"id"`
	CatID      int64   `json:"cat_id"`
	WeightKg   float64 `json:"weight_kg"`
	RecordedOn string  `json:"recorded_on"`
}

func basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || pass != os.Getenv("VIEW_PASSWORD") {
			w.Header().Set("WWW-Authenticate", `Basic realm="neko"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func catsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query("SELECT id, name FROM cats ORDER BY created_at ASC")
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		cats := []Cat{}
		for rows.Next() {
			var c Cat
			rows.Scan(&c.ID, &c.Name)
			cats = append(cats, c)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cats)

	case http.MethodPost:
		var c Cat
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Name == "" {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		res, err := db.Exec("INSERT INTO cats (name) VALUES (?)", c.Name)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		c.ID, _ = res.LastInsertId()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(c)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// /api/cats/{id}/weights
func catWeightsHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// parts: ["api","cats","{id}","weights"]
	catID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "invalid cat id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(
			"SELECT id, cat_id, weight_kg, DATE_FORMAT(recorded_on, '%Y-%m-%d') FROM weights WHERE cat_id=? ORDER BY recorded_on ASC",
			catID,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		ws := []Weight{}
		for rows.Next() {
			var wt Weight
			rows.Scan(&wt.ID, &wt.CatID, &wt.WeightKg, &wt.RecordedOn)
			ws = append(ws, wt)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ws)

	case http.MethodPost:
		var wt Weight
		if err := json.NewDecoder(r.Body).Decode(&wt); err != nil || wt.RecordedOn == "" || wt.WeightKg <= 0 {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		res, err := db.Exec(
			"INSERT INTO weights (cat_id, weight_kg, recorded_on) VALUES (?,?,?)",
			catID, wt.WeightKg, wt.RecordedOn,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		wt.ID, _ = res.LastInsertId()
		wt.CatID = catID
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(wt)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// /api/weights/{id}
func weightHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var wt Weight
		if err := json.NewDecoder(r.Body).Decode(&wt); err != nil || wt.RecordedOn == "" || wt.WeightKg <= 0 {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		_, err := db.Exec(
			"UPDATE weights SET weight_kg=?, recorded_on=? WHERE id=?",
			wt.WeightKg, wt.RecordedOn, id,
		)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		db.Exec("DELETE FROM weights WHERE id=?", id)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func main() {
	godotenv.Load()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "neko:neko@tcp(localhost:3306)/nekodb?parseTime=true"
	}
	if os.Getenv("VIEW_PASSWORD") == "" {
		log.Fatal("VIEW_PASSWORD is required")
	}

	var err error
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatal("cannot connect to db:", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/api/cats", basicAuth(http.HandlerFunc(catsHandler)))
	mux.Handle("/api/weights/", basicAuth(http.HandlerFunc(weightHandler)))
	mux.Handle("/api/cats/", basicAuth(http.HandlerFunc(catWeightsHandler)))
	mux.Handle("/", basicAuth(http.FileServer(http.Dir("./static"))))

	srv := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		log.Println("listening on :8080")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
	log.Println("server shutdown")
}
