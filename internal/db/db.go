package db

import (
	"log"
	"time"

	_ "github.com/heroku/x/hmetrics/onload"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/postgres"
	_ "github.com/lib/pq"
	"github.com/windoze95/culinaryai/internal/config"
)

func New(cfg *config.Config) (*gorm.DB, error) {
	return connectToDatabaseWithRetry(cfg.Env.DatabaseUrl.Value())
}

func connectToDatabaseWithRetry(dbURL string) (*gorm.DB, error) {
	var database *gorm.DB
	var err error

	start := time.Now()
	for {
		database, err = gorm.Open("postgres", dbURL)
		if err == nil {
			break
		}
		if time.Since(start) > 10*time.Minute {
			log.Fatalf("Error connecting to the database: %v", err)
		}
		log.Printf("Could not connect to database, retrying...")
		time.Sleep(5 * time.Second)
	}

	// Set a 5-second timeout for all queries in this session
	// db.Exec("SET statement_timeout = 5000")

	return database, err
}