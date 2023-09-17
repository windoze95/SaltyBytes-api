package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	goaway "github.com/TwiN/go-away"
	"github.com/asaskevich/govalidator"
	"github.com/windoze95/culinaryai/internal/config"
	"github.com/windoze95/culinaryai/internal/models"
	"github.com/windoze95/culinaryai/internal/openai"
	"github.com/windoze95/culinaryai/internal/repository"
	"github.com/windoze95/culinaryai/internal/util"
	"golang.org/x/crypto/bcrypt"
)

type UserService struct {
	Cfg  *config.Config
	Repo *repository.UserRepository
}

// Constructor function for initializing a new UserService
func NewUserService(cfg *config.Config, repo *repository.UserRepository) *UserService {
	return &UserService{
		Cfg:  cfg,
		Repo: repo,
	}
}

func (s *UserService) CreateUser(username, password string) error {
	// Validate username
	if err := s.ValidateUsername(username); err != nil {
		return err
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("Error hashing password: %v", err)
	}

	// Create User and UserSettings
	user := &models.User{
		Username:       username,
		HashedPassword: string(hashedPassword),
	}
	settings := &models.UserSettings{}

	if err := s.Repo.CreateUserAndSettings(user, settings); err != nil {
		return fmt.Errorf("Error creating user and settings: %v", err)
	}

	return nil
}

func (s *UserService) LoginUser(username, password string) (*models.User, error) {
	user, err := s.Repo.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.HashedPassword), []byte(password)); err != nil {
		return nil, errors.New("Invalid username or password")
	}

	return user, nil
}

func (s *UserService) GetPreloadedUserByID(sessionID uint) (*models.User, error) {
	return s.Repo.GetPreloadedUserByID(sessionID)
}

func (s *UserService) VerifyOpenAIKeyInUserSettings(user *models.User) (bool, error) {
	// Decrypt the OpenAI key
	decryptedKey, err := util.DecryptOpenAIKey(s.Cfg.Env.OpenAIKeyEncryptionKey.Value(), user.Settings.EncryptedOpenAIKey)
	if err != nil {
		return false, fmt.Errorf("failed to decrypt OpenAI key: %v", err)
	}

	// Verify the OpenAI key
	isValid, err := openai.VerifyOpenAIKey(decryptedKey)
	if err != nil {
		return false, fmt.Errorf("failed to verify OpenAI key: %v", err)
	}

	return isValid, nil
}

func (s *UserService) UpdateUserSettings(user *models.User, newOpenAIKey string) (bool, error) {
	// Encrypt the OpenAI key
	encryptedOpenAIKey, err := util.EncryptOpenAIKey(s.Cfg.Env.OpenAIKeyEncryptionKey.Value(), newOpenAIKey)
	if err != nil {
		return false, err
	}

	// Check if the OpenAI key has changed
	openAIKeyChanged := encryptedOpenAIKey != user.Settings.EncryptedOpenAIKey
	if openAIKeyChanged {
		if err := s.Repo.UpdateUserSettingsOpenAIKey(user.ID, encryptedOpenAIKey); err != nil {
			return false, err
		}
	}
	return openAIKeyChanged, nil
}

func (s *UserService) ValidateUsername(username string) error {
	exists, err := s.Repo.UsernameExists(username)
	if err != nil {
		return fmt.Errorf("Error checking username: %v", err)
	}
	if exists {
		return fmt.Errorf("Username is already taken")
	}

	minLength := 3
	if len(username) < minLength {
		return fmt.Errorf("username must be at least %d characters", minLength)
	}

	if !govalidator.IsAlphanumeric(username) {
		return fmt.Errorf("username can only contain alphanumeric characters")
	}

	var forbiddenUsernames = []string{
		"admin",
		"administrator",
		"root",
		// "julian",
		"awfulbits",
		"windoze95",
		"yana",
		"russianminx",
		"russianminxx",
		"sys",
		"sysadmin",
		"system",
		"test",
		"testuser",
		"test-user",
		"test_user",
		"login",
		"logout",
		"register",
		"password",
		"user",
		"user123",
		"newuser",
		"yourapp",
		"yourcompany",
		"yourbrand",
		"support",
		"help",
		"faq",
		"culinaryai",
		"culinary_ai",
		"culinary-ai",
		"culinaryaiadmin",
		"culinaryai_admin",
		"culinaryai-admin",
		"culinaryairoot",
		"culinaryai_root",
		"culinaryai-root",
	}

	lowercaseUsername := strings.ToLower(username)
	for _, forbiddenUsername := range forbiddenUsernames {
		if strings.EqualFold(lowercaseUsername, forbiddenUsername) {
			return fmt.Errorf("username '%s' is not allowed", username)
		}
	}

	// Profanity check using goaway library
	profanityDetector := goaway.NewProfanityDetector().WithSanitizeLeetSpeak(true).WithSanitizeSpecialCharacters(true).WithSanitizeAccents(false)
	if profanityDetector.IsProfane(username) {
		return fmt.Errorf("username contains inappropriate language")
	}

	// If we've passed all checks, the username is valid.
	return nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters long")
	}
	hasUppercase, _ := regexp.MatchString(`[A-Z]`, password)
	if !hasUppercase {
		return errors.New("password must contain at least one uppercase letter")
	}
	hasLowercase, _ := regexp.MatchString(`[a-z]`, password)
	if !hasLowercase {
		return errors.New("password must contain at least one lowercase letter")
	}
	hasNumber, _ := regexp.MatchString(`\d`, password)
	if !hasNumber {
		return errors.New("password must contain at least one digit")
	}
	hasSpecialChar, _ := regexp.MatchString(`[!@#$%^&*]`, password)
	if !hasSpecialChar {
		return errors.New("password must contain at least one special character")
	}
	return nil
}