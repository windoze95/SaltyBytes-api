package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/windoze95/saltybytes-api/internal/config"
	"github.com/windoze95/saltybytes-api/internal/models"
	"github.com/windoze95/saltybytes-api/internal/openai"
	"github.com/windoze95/saltybytes-api/internal/repository"
	"github.com/windoze95/saltybytes-api/internal/s3"
	"github.com/windoze95/saltybytes-api/internal/util"
)

type RecipeService struct {
	Cfg  *config.Config
	Repo *repository.RecipeRepository
}

// Constructor function for initializing a new RecipeService
func NewRecipeService(cfg *config.Config, repo *repository.RecipeRepository) *RecipeService {
	return &RecipeService{
		Cfg:  cfg,
		Repo: repo,
	}
}

func (s *RecipeService) GetRecipeByID(recipeID string) (*models.Recipe, error) {
	// Fetch the recipe by its ID from the repository
	recipe, err := s.Repo.GetRecipeByID(recipeID)
	if err != nil {
		return nil, err
	}

	// if recipe.GenerationComplete {

	// 	// Deserialize the GeneratedRecipeJSON field back into the GeneratedRecipe struct
	// 	if err := recipe.DeserializeGeneratedRecipe(); err != nil {
	// 		log.Printf("Failed to deserialize recipe: %v", err)
	// 		return nil, fmt.Errorf("failed to deserialize recipe: %w", err)
	// 	}
	// }

	return recipe, nil
}

func (s *RecipeService) CreateRecipe(user *models.User, userPrompt string) (*models.Recipe, error) {
	// Populate initial fields of the Recipe struct
	recipe := &models.Recipe{
		// GeneratedBy:       *user,
		// GeneratedBy: user,
		GeneratedByUserID: user.ID, // Set from user's ID
		UserPrompt:        userPrompt,
		GuidingContentID:  user.GuidingContent.ID, // Set from user's existing GuidingContent ID
		// GuidingContent:    user.GuidingContent, // Set from user's existing GuidingContent
		// GuidingContent:    &user.GuidingContent,    // Set from user's existing GuidingContent
		GuidingContentUID: user.GuidingContent.UID, // Set from user's existing GuidingContent
	}

	// Create a Recipe with the basic Recipe details
	if err := s.Repo.CreateRecipe(recipe); err != nil {
		return nil, fmt.Errorf("failed to save recipe record: %w", err)
	}

	// The recipe now has an ID generated by the database
	return recipe, nil
}

func (s *RecipeService) CompleteRecipeGeneration(recipe *models.Recipe, user *models.User) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Use a Done channel to signal completion
	done := make(chan bool)

	// Start the recipe generation process in a goroutine
	go func(ctx context.Context) {
		// Generate the full recipe
		// s.generateGeneratedRecipe(recipe, user, ctx)
		// Choose an api key
		key, err := chooseAPIKey(s.Cfg, user)
		if err != nil {
			log.Printf("error: %v", err)

			return
		}

		// Create a new chat service instance with the user's decrypted key
		chatService, err := openai.NewOpenaiClient(key)
		if err != nil {
			log.Printf("error: failed to create chat service: %v", err)
			// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create chat service: " + err.Error()})
			return
		}

		// Create the chat completion with the user's prompt
		RecipeManager, chatContext, err := chatService.CreateRecipeChatCompletion(recipe, user.GuidingContent)
		if err != nil {
			log.Printf("error: failed to create recipe chat completion: %v", err)
			// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recipe: " + err.Error()})
			return
		}

		// recipe.GeneratedRecipe = *recipeContent

		// // Serialize GeneratedRecipe to JSON
		// if err := recipe.SerializeGeneratedRecipe(); err != nil {
		// 	log.Printf("error: failed to serialize GeneratedRecipe: %v", err)
		// 	// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to serialize GeneratedRecipe: " + err.Error()})
		// 	return
		// }

		// if err := s.Repo.UpdateRecipeTitle(recipe, RecipeManager.Title); err != nil {
		// 	log.Printf("error: failed to update recipe title: %v", err)
		// 	return
		// }

		// Set the recipe core fields
		recipe.Title = RecipeManager.Title
		mainRecipeJSON, err := util.SerializeToJSONString(RecipeManager.MainRecipe)
		if err != nil {
			log.Printf("error: failed to serialize main recipe: %v", err)
			return
		}
		recipe.MainRecipeJSON = mainRecipeJSON
		subRecipesJSON, err := util.SerializeToJSONString(RecipeManager.SubRecipes)
		if err != nil {
			log.Printf("error: failed to serialize sub recipes: %v", err)
			return
		}
		recipe.SubRecipesJSON = subRecipesJSON
		recipe.ImagePrompt = RecipeManager.ImagePrompt
		recipe.ChatContext = chatContext

		// Update the existing recipe's core fields
		if err := s.Repo.UpdateRecipeCoreFields(recipe); err != nil {
			log.Printf("error: failed to update recipe core fields: %v", err)
			return
		}

		// Associate tags with the recipe
		if err := s.AssociateTagsWithRecipe(recipe, RecipeManager.Hashtags); err != nil {
			log.Printf("error: failed to associate tags with recipe: %v", err)
			return
		}

		imageService, err := openai.NewOpenaiClient(key)
		if err != nil {
			log.Printf("error: failed to create image service: %v", err)
			// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create image service: " + err.Error()})
			return
		}

		imageBytes, err := imageService.CreateImage(RecipeManager.ImagePrompt)
		if err != nil {
			log.Printf("error: failed to create recipe image: %v", err)
			// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recipe image: " + err.Error()})
			return
		}

		s3Key := s3.GenerateS3Key(recipe.ID)

		imageURL, err := s3.UploadRecipeImageToS3(s.Cfg, imageBytes, s3Key)
		if err != nil {
			log.Printf("error: failed to upload image to S3: %v", err)
			// c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload image to S3: " + err.Error()})
			return
		}

		// Update the ImageURL field in the database using the repository
		if err := s.Repo.UpdateRecipeImageURL(recipe, imageURL); err != nil {
			log.Printf("error: failed to update recipe with image URL: %v", err)
			return
		}

		// Signal completion
		done <- true
	}(ctx)

	// Wait for the goroutine to finish or timeout
	select {
	case success := <-done:
		if success {
			// Mark the generation as complete
			if err := s.Repo.UpdateRecipeGenerationStatus(recipe, true); err != nil {
				// Log error
				log.Println("error: Failed to update GenerationComplete:", err)
			}
		} else {
			// Log the failure case
			// More specific logging of the error is handled in the goroutine
			log.Println("error: Failed to generate recipe")
		}
	case <-ctx.Done():
		// Log the timeout case
		log.Println("error: Incomplete recipe generation: timed out after 5 minutes")
	}

	// Close the Done channel
	close(done)
}

// Checks if each hashtag exists as a Tag in the database.
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

	recipe.Hashtags = associatedTags
	if err := s.Repo.UpdateRecipeTagsAssociation(recipe, associatedTags); err != nil {
		return fmt.Errorf("failed to update recipe with tags: %v", err)
	}

	return nil
}

func chooseAPIKey(cfg *config.Config, user *models.User) (string, error) {
	var key string
	if user.Settings.EncryptedOpenAIKey != "" {
		decryptedKey, err := util.DecryptOpenAIKey(cfg.Env.OpenAIKeyEncryptionKey.Value(), user.Settings.EncryptedOpenAIKey)
		if err != nil {
			return "", fmt.Errorf("failed to decrypt API key: %v", err)
		}
		key = decryptedKey
	} else {
		key = cfg.Env.PublicOpenAIKey.Value()
	}
	return key, nil
}

func cleanHashtag(hashtag string) string {
	// Convert to lowercase
	hashtag = strings.ToLower(hashtag)

	// Remove spaces
	hashtag = strings.ReplaceAll(hashtag, " ", "")

	// Remove '#' if present
	hashtag = strings.TrimPrefix(hashtag, "#")

	return hashtag
}
