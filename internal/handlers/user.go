package handlers

import (
	"fmt"
	"log"
	"net/http"

	"github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"github.com/windoze95/saltybytes-api/internal/service"
	"github.com/windoze95/saltybytes-api/internal/util"
)

type UserHandler struct {
	Service *service.UserService
}

func NewUserHandler(userService *service.UserService) *UserHandler {
	return &UserHandler{Service: userService}
}

func (h *UserHandler) CreateUser(c *gin.Context) {
	var newUser struct {
		Username  string `json:"username" binding:"required"`
		FirstName string `json:"firstName"`
		Email     string `json:"email" binding:"required"`
		Password  string `json:"password" binding:"required"`
		// Recaptcha string `json:"recaptcha" binding:"required"`
	}

	// Returns error if a required field is not included
	if err := c.ShouldBindJSON(&newUser); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username, email, and password fields are required"})
		return
	}

	// // Verify reCAPTCHA
	// if err := h.Service.VerifyRecaptcha(newUser.Recaptcha); err != nil {
	// 	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	// 	return
	// }

	// Validate username
	if err := h.Service.ValidateUsername(newUser.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate email
	if err := h.Service.ValidateEmail(newUser.Email); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate password
	if err := h.Service.ValidatePassword(newUser.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create user
	user, err := h.Service.CreateUser(newUser.Username, newUser.FirstName, newUser.Email, newUser.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Log the user in
	tokenString, err := generateAuthToken(user.ID, h.Service.Cfg.Env.JwtSecretKey.Value())
	if err != nil {
		log.Printf("error: handlers.LoginUser: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"access_token": tokenString, "message": "User signed up successfully", "user": user})
}

func (h *UserHandler) LoginUser(c *gin.Context) {
	var userCredentials struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&userCredentials); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "All fields are required"})
		return
	}

	user, err := h.Service.LoginUser(userCredentials.Username, userCredentials.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	// // Create a new session
	// session := c.MustGet("session").(*sessions.Session)
	// session.Values["user_id"] = user.ID
	// session.Values["ip"] = c.ClientIP()
	// session.Values["user_agent"] = c.Request.UserAgent()

	// // Save the session
	// session.Save(c.Request, c.Writer)

	// c.JSON(http.StatusOK, gin.H{"message": "User logged in successfully"})

	// Log the user in
	tokenString, err := generateAuthToken(user.ID, h.Service.Cfg.Env.JwtSecretKey.Value())
	if err != nil {
		log.Printf("error: handlers.LoginUser: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// c.SetCookie(
	// 	"auth_token",      // Cookie name
	// 	tokenString,       // Cookie value
	// 	31536000,          // Max age in seconds (365 days)
	// 	"/",               // Path
	// 	".api.saltybytes.ai", // Domain, set with leading dot for subdomain compatibility
	// 	true,              // Secure
	// 	true,              // HTTP only
	// )

	// http.SetCookie(c.Writer, &http.Cookie{
	// 	Name:     "auth_token",
	// 	Value:    tokenString,
	// 	HttpOnly: true,
	// 	Secure:   true,
	// 	Path:     "/",
	// })

	// c.JSON(http.StatusOK, gin.H{"message": "User logged in successfully", "user": user})
	c.JSON(http.StatusOK, gin.H{"access_token": tokenString, "message": "User logged in successfully", "user": user})
}

func generateAuthToken(userID uint, secretKey string) (string, error) {
	// Create a new token object, specifying signing method and the claims you would like it to contain.
	token := jwt.New(jwt.SigningMethodHS256)

	// Set claims
	claims := token.Claims.(jwt.MapClaims)
	claims["user_id"] = userID

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", fmt.Errorf("generateAuthToken: %v ", err)
	}

	return tokenString, nil
}

func (h *UserHandler) VerifyToken(c *gin.Context) {
	// Retrieve the user from the context
	user, _ := util.GetUserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"isAuthenticated": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"isAuthenticated": true, "user": user})
}

func (h *UserHandler) LogoutUser(c *gin.Context) {
	// Clear the cookie
	util.ClearAuthTokenCookie(c)

	c.JSON(http.StatusOK, gin.H{"message": "User logged out successfully"})
}

func (h *UserHandler) GetUserByID(c *gin.Context) {
	// Retrieve the user from the context
	user, err := util.GetUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": user})
}

func (h *UserHandler) GetUserSettings(c *gin.Context) {
	// Retrieve the user from the context
	user, err := util.GetUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Use the service to get and verify the settings
	isValid, err := h.Service.VerifyOpenAIKeyInUserSettings(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"isValid": isValid, "user": user})
}

func (h *UserHandler) UpdateUserSettings(c *gin.Context) {
	// Retrieve the user from the context
	user, err := util.GetUserFromContext(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Parse the new OpenAI key from the request body
	var newSettings struct {
		OpenAIKey string `json:"apikey"`
	}
	if err := c.ShouldBindJSON(&newSettings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	openAIKeyChanged, err := h.Service.UpdateUserSettings(user, newSettings.OpenAIKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings: " + err.Error()})
		return
	}

	// This won't seem as redundant when more settings are added
	if openAIKeyChanged {
		c.JSON(http.StatusOK, gin.H{"message": "Settings updated successfully"})
	} else {
		c.JSON(http.StatusOK, gin.H{"message": "No changes made"})
	}
}

// func (h *UserHandler) UpdatePreferences(c *gin.Context) {
// 	// Parse request to get 'preferences' data

// 	// Call the service layer function to perform the actual update
// 	if err := h.Service.UpdateGuidingContent(userID, updatedGC); err != nil {
// 		// Handle error
// 	}

// 	// Respond back to client
// }
