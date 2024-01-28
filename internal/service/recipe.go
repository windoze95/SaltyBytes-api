package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jinzhu/gorm"
	"github.com/windoze95/saltybytes-api/internal/config"
	"github.com/windoze95/saltybytes-api/internal/models"
	"github.com/windoze95/saltybytes-api/internal/openai"
	"github.com/windoze95/saltybytes-api/internal/repository"
	"github.com/windoze95/saltybytes-api/internal/s3"
)

// RecipeService is the business logic layer for recipe-related operations.
type RecipeService struct {
	Cfg  *config.Config
	Repo *repository.RecipeRepository
}

// RecipeResponse is the response object for recipe-related operations.
type RecipeResponse struct {
	ID                     uint               `json:"ID"`
	Title                  string             `json:"title"`
	Ingredients            models.Ingredients `json:"ingredients"`
	Instructions           []string           `json:"instructions"`
	CookTime               int                `json:"cook_time"`
	UnitSystem             models.UnitSystem  `json:"unit_system"`
	LinkedRecipes          []*models.Recipe   `json:"linked_recipes"`
	LinkSuggestions        []string           `json:"link_suggestions"`
	Hashtags               []*models.Tag      `json:"hashtags"`
	ImageURL               string             `json:"image_url"`
	CreatedByID            uint               `json:"created_by_id"`
	CreatedByUsername      string             `json:"created_by_username"`
	HistoryID              uint               `json:"chat_history_id"`
	ForkedFromID           *uint              `json:"forked_from_id"`
	ForkedFromName         *string            `json:"forked_from_name"`
	UserUnitSystem         models.UnitSystem  `json:"user_unit_system"`
	PersonalizationUID     uuid.UUID          `json:"personalization_uid"`
	UserPersonalizationUID uuid.UUID          `json:"user_personalization_uid"`
}

// NewRecipeService is the constructor function for initializing a new RecipeService
func NewRecipeService(cfg *config.Config, repo *repository.RecipeRepository) *RecipeService {
	return &RecipeService{
		Cfg:  cfg,
		Repo: repo,
	}
}

// GetRecipeByID fetches a recipe by its ID.
func (s *RecipeService) GetRecipeByID(recipeID uint) (*RecipeResponse, error) {
	// Fetch the recipe by its ID from the repository
	recipe, err := s.Repo.GetRecipeByID(recipeID)
	if err != nil {
		return nil, err
	}

	// Create a RecipeResponse from the Recipe
	recipeResponse := toRecipeResponse(recipe)

	return recipeResponse, nil
}

// HistoryResponse is the response object for recipe history-related operations.
type HistoryResponse struct {
	Entries []models.RecipeHistoryEntry `json:"entries"`
}

// GetRecipeHistoryByID fetches a recipe history by its ID.
func (s *RecipeService) GetRecipeHistoryByID(historyID uint) (*HistoryResponse, error) {
	// Fetch the recipe by its ID from the repository
	history, err := s.Repo.GetHistoryByID(historyID)
	if err != nil {
		return nil, err
	}

	historyResponse := &HistoryResponse{Entries: history.Entries}

	return historyResponse, nil
}

// InitGenerateRecipeWithChat initializes a new recipe with chat.
func (s *RecipeService) InitGenerateRecipeWithChat(user *models.User) (*RecipeResponse, *models.Recipe, error) {
	if user.Personalization.ID == 0 {
		log.Printf("user %d Personalization is nil", user.ID)
		return nil, nil, errors.New("user's Personalization is nil")
	}

	// Populate initial fields of the Recipe struct
	recipe := &models.Recipe{
		CreatedBy:          user,
		PersonalizationUID: user.Personalization.UID, // Set from user's existing Personalization
		History: &models.RecipeHistory{
			Entries: []models.RecipeHistoryEntry{},
		},
	}

	// Create a Recipe with the basic Recipe details
	if err := s.Repo.CreateRecipe(recipe); err != nil {
		return nil, nil, fmt.Errorf("failed to save recipe record: %w", err)
	}

	recipeResponse := toRecipeResponse(recipe)

	// The recipe now has an ID generated by the database
	return recipeResponse, recipe, nil
}

// FinishGenerateRecipeWithChat finishes generating a recipe with chat.
func (s *RecipeService) FinishGenerateRecipeWithChat(recipe *models.Recipe, user *models.User, userPrompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	recipeErrChan := make(chan error)
	imageErrChan := make(chan error)

	recipeManager := &openai.RecipeManager{
		UserPrompt:   userPrompt,
		UnitSystem:   user.Personalization.GetUnitSystemText(),
		Requirements: user.Personalization.Requirements,
		Cfg:          s.Cfg,
	}

	// Goroutine to handle recipe generation
	go func(ctx context.Context, recipeErrChan chan<- error, imageErrChan chan<- error) {
		if err := recipeManager.GenerateRecipeWithChat(); err != nil {
			recipeErrChan <- err
			return
		}

		// Goroutine to handle image generation and upload
		go func(ctx context.Context, imageErrChan chan<- error) {
			if err := recipeManager.GenerateRecipeImage(); err != nil {
				imageErrChan <- err
				return
			}

			imageErrChan <- nil
		}(ctx, imageErrChan)

		if err := populateRecipeCoreFields(recipe, recipeManager); err != nil {
			recipeErrChan <- err
			return
		}

		if err := s.Repo.UpdateRecipeDef(recipe, recipeManager.NextRecipeHistoryEntry); err != nil {
			recipeErrChan <- err
			return
		}

		if err := s.AssociateTagsWithRecipe(recipe, recipeManager.RecipeDef.Hashtags); err != nil {
			log.Println(err)
		}

		recipeErrChan <- nil
	}(ctx, recipeErrChan, imageErrChan)

	// Wait for the recipe generation goroutine to finish or timeout
	select {
	case err := <-recipeErrChan:
		if err != nil {
			log.Printf("error: %v", err)
			e := s.DeleteRecipe(recipe.ID)
			if e != nil {
				log.Printf("error: failed to delete recipe: %v", e)
				return
			}
			log.Printf("recipe %d deleted", recipe.ID)
			return
		}
		// Offloading failed recipes to frontend, Frontend will look for new recipe history entries
		// if err := s.Repo.UpdateRecipeGenerationStatus(recipe.ID, true); err != nil {
		// 	log.Printf("error: failed to update GenerationComplete: %v", err)
		// 	return
		// }
	case <-ctx.Done():
		err := errors.New("incomplete recipe generation: timed out after 5 minutes")
		log.Printf("error: %v", err)
		e := s.DeleteRecipe(recipe.ID)
		if e != nil {
			log.Printf("error: failed to delete recipe: %v", e)
			return
		}
		log.Printf("recipe %d deleted", recipe.ID)
		return
	}

	// Wait for the image generation goroutine to finish or timeout
	select {
	case err := <-imageErrChan:
		if err != nil {
			log.Println(err)
			return
		}

		var recipeImageURL string
		if imageURL, err := uploadRecipeImage(recipe.ID, recipeManager, s.Cfg); err != nil {
			log.Println(err)
			return
		} else {
			recipeImageURL = imageURL
		}

		if err := s.Repo.UpdateRecipeImageURL(recipe.ID, recipeImageURL); err != nil {
			log.Println(err)
			return
		}
	case <-ctx.Done():
		err := errors.New("incomplete recipe image generation: timed out after 5 minutes")
		log.Println(err)
		return
	}
}

// DeleteRecipe deletes a recipe by its ID.
func (s *RecipeService) DeleteRecipe(recipeID uint) error {
	// Delete the recipe from the database
	if err := s.Repo.DeleteRecipe(recipeID); err != nil {
		return fmt.Errorf("failed to delete recipe: %w", err)
	}

	// Delete the recipe image from S3
	s3Key := s3.GenerateS3Key(recipeID)
	if err := s3.DeleteRecipeImageFromS3(s.Cfg, s3Key); err != nil {
		return fmt.Errorf("failed to delete recipe image from S3: %w", err)
	}

	return nil
}

// populateRecipeFields populates the fields of the Recipe struct.
func populateRecipeCoreFields(recipe *models.Recipe, recipeManager *openai.RecipeManager) error {
	// ingredientsJSON, err := util.SerializeToJSONString(recipeManager.RecipeDef.Ingredients)
	// if err != nil {
	// 	return errors.New("failed to serialize ingredients: " + err.Error())
	// }
	recipe.Title = recipeManager.RecipeDef.Title
	recipe.Ingredients = recipeManager.RecipeDef.Ingredients
	recipe.Instructions = recipeManager.RecipeDef.Instructions
	recipe.CookTime = recipeManager.RecipeDef.CookTime
	recipe.LinkSuggestions = recipeManager.RecipeDef.LinkedRecipeSuggestions
	recipe.ImagePrompt = recipeManager.RecipeDef.ImagePrompt

	if recipe.History == nil {
		return errors.New("recipe history is nil")
	}

	// Append the new entry history to the existing entries history
	recipe.History.Entries = append(recipe.History.Entries, recipeManager.RecipeHistoryEntries...)

	return validateRecipeCoreFields(recipe)
}

// validateRecipeFields validates that the Recipe's required fields are populated.
func validateRecipeCoreFields(recipe *models.Recipe) error {
	if recipe.Title == "" ||
		recipe.Ingredients == nil ||
		recipe.Instructions == nil ||
		recipe.ImagePrompt == "" ||
		recipe.History.Entries == nil {
		return errors.New("missing required fields in Recipe")
	}

	return nil
}

// uploadRecipeImage uploads the recipe image to S3 and returns the new image URL.
func uploadRecipeImage(recipeId uint, recipeManager *openai.RecipeManager, cfg *config.Config) (string, error) {
	s3Key := s3.GenerateS3Key(recipeId)
	imageURL, err := s3.UploadRecipeImageToS3(cfg, recipeManager.ImageBytes, s3Key)
	if err != nil {
		return "", errors.New("failed to upload image to S3: " + err.Error())
	}

	return imageURL, nil
}

// AssociateTagsWithRecipe checks if each hashtag exists as a Tag in the database.
// If it does, it uses the existing Tag's ID and Name.
func (s *RecipeService) AssociateTagsWithRecipe(recipe *models.Recipe, tags []string) error {
	var associatedTags []models.Tag

	for _, hashtag := range tags {
		cleanedHashtag := cleanHashtag(hashtag)

		// Search for the tag by the cleaned name
		existingTag, err := s.Repo.FindTagByName(cleanedHashtag)
		if err == nil {
			associatedTags = append(associatedTags, *existingTag)
		} else if gorm.IsRecordNotFoundError(err) {
			newTag := models.Tag{Hashtag: cleanedHashtag}
			if err := s.Repo.CreateTag(&newTag); err != nil {
				return fmt.Errorf("failed to create new tag: %v", err)
			}
			associatedTags = append(associatedTags, newTag)
		} else {
			return fmt.Errorf("database error while searching for tag: %v", err)
		}
	}

	if err := s.Repo.UpdateRecipeTagsAssociation(recipe.ID, associatedTags); err != nil {
		return fmt.Errorf("failed to update recipe with tags: %v", err)
	}
	// recipe.Hashtags = associatedTags

	return nil
}

// toRecipeResponse converts a Recipe to a RecipeResponse
func toRecipeResponse(r *models.Recipe) *RecipeResponse {
	var forkedFromID *uint
	if r.ForkedFromID != nil && *r.ForkedFromID != 0 {
		forkedFromID = r.ForkedFromID
	}

	var forkedFromName *string
	if r.ForkedFrom != nil {
		forkedFromName = &r.ForkedFrom.Title
	}

	return &RecipeResponse{
		ID:                 r.ID,
		Title:              r.Title,
		Ingredients:        r.Ingredients,
		Instructions:       r.Instructions,
		CookTime:           r.CookTime,
		UnitSystem:         r.UnitSystem,
		LinkedRecipes:      r.LinkedRecipes,
		LinkSuggestions:    r.LinkSuggestions,
		Hashtags:           r.Hashtags,
		ImageURL:           r.ImageURL,
		CreatedByID:        r.CreatedByID,
		CreatedByUsername:  r.CreatedBy.Username,
		HistoryID:          r.HistoryID,
		ForkedFromID:       forkedFromID,
		ForkedFromName:     forkedFromName,
		PersonalizationUID: r.PersonalizationUID,
	}
}

// cleanHashtag formats a hashtag string.
func cleanHashtag(hashtag string) string {
	// Convert to lowercase
	hashtag = strings.ToLower(hashtag)

	// Remove spaces
	hashtag = strings.ReplaceAll(hashtag, " ", "")

	// Remove '#' if present
	hashtag = strings.TrimPrefix(hashtag, "#")

	return hashtag
}
