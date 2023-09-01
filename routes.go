package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"golang.org/x/time/rate"
)

type PageData struct {
	TitlePrefix string
	User        *User
}

type limiterInfo struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func startGin() {
	// Set Gin mode and create default router
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// Define constants and variables related to rate limiting
	var rps int = 1                        // 1 request per second
	var burst int = 5                      // Burst of 5 requests
	var cleanupInterval = 10 * time.Minute // Cleanup every 10 minutes
	var expiration = 1 * time.Hour         // Remove unused limiters after 1 hour

	// Define middleware functions related to rate limiting
	publicOpenAIKeyRateLimiter := rate.NewLimiter(rate.Limit(rps), burst)
	// Rate limiting middleware specific to users with no OpenAI key
	publicOpenAIKeyRateLimitMiddleware := func(c *gin.Context) {
		// Retrieve the user from the context
		user, err := getUserFromContext(c)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if user.Settings.EncryptedOpenAIKey == "" {
			// Apply rate limiting and use shared key
			if !publicOpenAIKeyRateLimiter.Allow() {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "429: Too many requests"})
				c.Abort()
				return
			}
		}

		c.Next()
	}

	// Register global middleware functions with router
	router.Use(RateLimitByIPMiddleware(rps, cleanupInterval, expiration))
	router.Use(SessionMiddleware())

	// Register static files and templates with router
	router.LoadHTMLGlob("templates/*.tmpl")
	router.Static("/static", "static")

	router.GET("/", UserMiddleware(), func(c *gin.Context) {
		val, _ := c.Get("user") // it's okay if no value exists

		user, _ := val.(*User) // if not 'ok', 'user' is nil struct as expected

		pageData := PageData{
			TitlePrefix: gc.Title,
			User:        user,
		}
		c.HTML(http.StatusOK, "index.tmpl", pageData)
	})

	// Registration page
	router.GET("/signup", func(c *gin.Context) {
		c.HTML(http.StatusOK, "signup.tmpl", gin.H{})
	})

	// User sign up
	router.POST("/users", signupUserHandler)

	// Login page
	router.GET("/login", func(c *gin.Context) {
		c.HTML(http.StatusOK, "login.tmpl", gin.H{})
	})

	// User login
	router.POST("/login", UserMiddleware(), loginUserHandler)

	// Settings modal
	router.GET("/settings", UserMiddleware(), getSettingsHandler)

	// User settings
	router.PUT("/users/settings", UserMiddleware(), updateUserSettingsHandler)

	// Viewing a single recipe by its ID
	router.GET("/recipes/:recipe_id", UserMiddleware(), viewRecipeHandler)

	// Recipe generation
	router.POST("/recipes", UserMiddleware(), publicOpenAIKeyRateLimitMiddleware, generateRecipeHandler)

	// Viewing recipes generated by a user
	router.GET("/users/:id/recipes", UserMiddleware(), func(c *gin.Context) {
		// This new route might be used to get all recipes generated by a specific user.
	})

	// Viewing the recipes collected by a user
	router.GET("/users/:id/collected", UserMiddleware(), func(c *gin.Context) {
		// This new route might be used to get all recipes collected by a specific user.
	})

	// Viewing saved recipes from all users
	router.GET("/recipes/collected", func(c *gin.Context) {})

	// Viewing trashed recipes from all users (Deletes on expiration)
	// router.GET("/recipes/trash", func(c *gin.Context) {})

	// Collecting any existing recipe
	router.PUT("/users/:id/recipes/:recipe_id/collect", UserMiddleware(), UserOwnerMiddleware(), collectRecipeHandler)

	// Deleting a recipe by the user who generated it
	router.DELETE("/users/:id/recipes/:recipe_id", func(c *gin.Context) {
		userID, err := strconv.ParseUint(c.Param("id"), 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
			return
		}
		recipeID := c.Param("recipe_id")

		var recipe Recipe
		if err := db.Where("id = ?", recipeID).First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Recipe not found"})
			return
		}

		if recipe.GeneratedByUserID != uint(userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "User not authorized to delete this recipe"})
			return
		}
		timeNow := time.Now()
		recipe.DeletedAt = &timeNow
		db.Save(&recipe)

		c.JSON(http.StatusOK, gin.H{"message": "Recipe deleted"})
	})

	// Collecting a recipe from the trash
	router.PUT("/users/:id/trash/:recipe_id/collect", func(c *gin.Context) {
		userID := c.Param("id")
		recipeID := c.Param("recipe_id")

		var recipe Recipe
		if err := db.Where("id = ?", recipeID).First(&recipe).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Recipe not found"})
			return
		}

		if recipe.DeletedAt == nil || recipe.DeletedAt.AddDate(0, 0, 30).Before(time.Now()) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Recipe is not in trash"})
			return
		}

		var user User
		if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		recipe.GeneratedBy = nil
		recipe.DeletedAt = nil
		user.CollectedRecipes = append(user.CollectedRecipes, recipe)

		db.Save(&user)
		db.Save(&recipe)

		c.JSON(http.StatusOK, gin.H{"message": "Recipe collected from trash"})
	})

	// Viewing trashed recipes from all users
	router.GET("/recipes/trash", func(c *gin.Context) {
		var recipes []Recipe
		db.Where("deleted_at IS NOT NULL AND deleted_at > ?", time.Now().AddDate(0, 0, -30)).Find(&recipes)

		c.JSON(http.StatusOK, gin.H{"recipes": recipes})
	})

	port := os.Getenv(gc.Env.Port)
	if port == "" {
		log.Fatalf("%s must be set", gc.Env.Port)
	}
	router.Run(":" + port)
}

func RateLimitByIPMiddleware(rps int, cleanupInterval time.Duration, expiration time.Duration) gin.HandlerFunc {
	var limiters sync.Map

	// Cleanup goroutine
	go func() {
		for range time.Tick(cleanupInterval) {
			limiters.Range(func(key, value interface{}) bool {
				if time.Since(value.(*limiterInfo).lastSeen) > expiration {
					limiters.Delete(key)
				}
				return true
			})
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()

		// Use LoadOrStore to ensure thread safety
		actual, _ := limiters.LoadOrStore(ip, &limiterInfo{
			limiter:  rate.NewLimiter(rate.Limit(rps), rps),
			lastSeen: time.Now(),
		})

		info := actual.(*limiterInfo)
		info.lastSeen = time.Now()

		if !info.limiter.Allow() {
			// Too many requests
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
			c.Abort()
			return
		}

		c.Next()
	}
}

func SessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := store.Get(c.Request, "session")
		if err != nil {
			// Handle error. For example:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get session"})
			c.Abort()
			return
		}

		c.Set("session", session)

		c.Next()
	}
}

func UserOwnerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Retrieve user's session
		session := c.MustGet("session").(*sessions.Session)

		// Extract authenticated user's ID from the session
		authenticatedUserID := session.Values["user_id"]

		// Extract user ID from URL
		userID := c.Param("id")

		// Check if the authenticated user's ID matches the user ID in the URL
		if authenticatedUserID != userID {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "You are not authorized to perform this action."})
			c.Abort()
			return
		}

		c.Next()
	}
}

func UserMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := getPreloadSessionUserOrNil(c)
		if user == nil {
			c.Set("user", nil)
		} else {
			c.Set("user", user)
		}

		c.Next()
	}
}

func getPreloadSessionUserOrNil(c *gin.Context) *User {
	session := c.MustGet("session").(*sessions.Session)
	userID, ok := session.Values["user_id"].(uint) // Adjust the type as needed
	if !ok || userID == 0 {
		return nil
	}

	user := &User{}
	if err := db.Preload("Settings").Preload("GuidingContent").Where("id = ?", userID).First(&user).Error; err != nil {
		// If no user is found in the database, return nil
		return nil
	}

	return user
}
