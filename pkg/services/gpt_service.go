package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type ThreadManager struct {
	ThreadID string
}

var (
	threadManagers = make(map[string]*ThreadManager)
	mutex          sync.Mutex
)

// Define InitializeRequest in services.go
type InitializeRequest struct {
	SystemInstructions string `json:"system_instructions"`
	VideoID            string `json:"video_id"`
	Title              string `json:"title"`
	Channel            string `json:"channel"`
	Transcript         string `json:"transcript"`
}

// Initialize the OpenAI client and load the API key
func InitOpenAIClient() {
	if err := godotenv.Load("../.env"); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}
}

// CreateAssistantWithMetadata creates a new assistant based on YouTube video metadata
func CreateAssistantWithMetadata(initReq InitializeRequest) (string, error) {
	url := "https://api.openai.com/v1/assistants"

	requestBody := map[string]interface{}{
		"model":        "gpt-4o-mini",
		"name":         initReq.VideoID,
		"instructions": fmt.Sprintf("You are a helpful assistant for the video titled '%s' by '%s'. Here is the transcript: %s", initReq.Title, initReq.Channel, initReq.Transcript),
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create assistant: %s", string(bodyBytes))
	}

	var createResp struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&createResp)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	// Store the assistant ID in Redis
	err = RedisClient.Set(Ctx, "assistant_id:"+initReq.VideoID, createResp.ID, 24*time.Hour).Err()
	if err != nil {
		return "", fmt.Errorf("failed to store assistant ID in Redis: %v", err)
	}

	return createResp.ID, nil
}

// AskAssistantQuestion adds a question to the thread and gets a response
func AskAssistantQuestion(videoID, assistantID, question string) (string, error) {
	threadManager, err := GetOrCreateThreadManager(videoID, assistantID)
	if err != nil {
		return "", fmt.Errorf("failed to get thread manager: %v", err)
	}

	err = threadManager.AddMessageToThread("user", question, videoID)
	if err != nil {
		return "", fmt.Errorf("failed to add message: %v", err)
	}

	return threadManager.RunAssistant(assistantID, videoID)
}

// GetOrCreateThreadManager retrieves the thread from Redis or creates a new one if it doesn't exist
func GetOrCreateThreadManager(videoID string, assistantID string) (*ThreadManager, error) {
	mutex.Lock()
	defer mutex.Unlock()

	// Check if a thread ID already exists in Redis
	threadID, err := RedisClient.Get(Ctx, "thread_id:"+videoID).Result()
	if err != nil {
		fmt.Printf("Error type: %T\n", err) // Print the type of the error
		if err.Error() == "redis: nil" {
			log.Println("Redis key not found for videoID:", videoID)

			// Create a new thread if no thread exists
			threadID, err = createThread()
			if err != nil {
				return nil, fmt.Errorf("failed to create thread: %v", err)
			}

			// Log the newly created thread ID
			log.Printf("New thread created with ID: %s for video: %s", threadID, videoID)

			// Store the new thread ID in Redis
			err = RedisClient.Set(Ctx, "thread_id:"+videoID, threadID, 24*time.Hour).Err()
			if err != nil {
				log.Printf("Failed to store thread ID in Redis for video %s: %v", videoID, err)
				return nil, fmt.Errorf("failed to store thread ID in Redis: %v", err)
			}

			log.Printf("Thread ID successfully stored in Redis for video %s", videoID)
		} else {
			log.Printf("Error retrieving thread ID from Redis for video %s: %v", videoID, err)
			return nil, fmt.Errorf("failed to retrieve thread ID from Redis: %v", err)
		}
	} else {
		log.Printf("Thread ID %s retrieved from Redis for video %s", threadID, videoID)
	}

	// Create a ThreadManager instance
	tm := &ThreadManager{ThreadID: threadID}
	threadManagers[assistantID] = tm
	return tm, nil
}

func createThread() (string, error) {
	url := "https://api.openai.com/v1/threads"
	requestBody := map[string]interface{}{}

	body, err := json.Marshal(requestBody)
	if err != nil {
		log.Printf("Failed to marshal thread creation request: %v", err)
		return "", fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Failed to create HTTP request for thread creation: %v", err)
		return "", fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send thread creation request: %v", err)
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Printf("Thread creation failed with status code %d: %s", resp.StatusCode, string(bodyBytes))
		return "", fmt.Errorf("failed to create thread: %s", string(bodyBytes))
	}

	var threadResp struct {
		ID string `json:"id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&threadResp)
	if err != nil {
		log.Printf("Failed to decode thread creation response: %v", err)
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	log.Printf("Thread created with ID %s", threadResp.ID)
	return threadResp.ID, nil
}

// Storing each interaction message in Redis
func (tm *ThreadManager) AddMessageToThread(role, content, videoID string) error {
	url := fmt.Sprintf("https://api.openai.com/v1/threads/%s/messages", tm.ThreadID)

	// Log the message being added
	log.Printf("Adding message to thread. Role: %s, Content: %s, VideoID: %s", role, content, videoID)

	requestBody := map[string]interface{}{
		"role":    role,
		"content": content,
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Printf("Failed to add message to thread. StatusCode: %d, Response: %s", resp.StatusCode, string(bodyBytes))
		return fmt.Errorf("failed to add message to thread: %s", string(bodyBytes))
	}

	// Log success in adding message to thread
	log.Printf("Message added to thread. Role: %s, Content: %s, VideoID: %s", role, content, videoID)

	// Store the interaction message in Redis under the videoID key
	err = RedisClient.RPush(Ctx, "interactions:"+videoID, content).Err()
	if err != nil {
		log.Printf("Failed to store interaction in Redis for VideoID %s: %v", videoID, err)
		return fmt.Errorf("failed to store interaction in Redis: %v", err)
	}

	// Log success of Redis storage
	log.Printf("Interaction message stored in Redis for VideoID: %s", videoID)
	return nil
}

func (tm *ThreadManager) RunAssistant(assistantID string, videoID string) (string, error) {
	url := fmt.Sprintf("https://api.openai.com/v1/threads/%s/runs", tm.ThreadID)

	requestBody := map[string]interface{}{
		"assistant_id": assistantID,
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to run assistant: %s", string(bodyBytes))
	}

	var runResp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	err = json.NewDecoder(resp.Body).Decode(&runResp)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	// Poll for completion
	for {
		time.Sleep(2 * time.Second)
		status, err := tm.GetRunStatus(runResp.ID)
		if err != nil {
			return "", fmt.Errorf("failed to get run status: %v", err)
		}

		if status == "completed" {
			messages, err := tm.GetThreadMessages()
			if err != nil {
				return "", fmt.Errorf("failed to get thread messages: %v", err)
			}

			// Return the assistant message
			for _, msg := range messages {
				if msg.Role == "assistant" {
					var assistantResponse string
					for _, fragment := range msg.Content {
						if fragment.Type == "text" && fragment.Text != nil {
							assistantResponse += fragment.Text.Value
						}
					}
					// Store assistant's response in Redis
					err = RedisClient.RPush(Ctx, "interactions:"+videoID, "Assistant: "+assistantResponse).Err()
					if err != nil {
						log.Printf("Failed to store assistant response in Redis for ThreadID %s: %v", tm.ThreadID, err)
						return "", fmt.Errorf("failed to store assistant response in Redis: %v", err)
					}

					log.Printf("Assistant response stored in Redis for ThreadID: %s", tm.ThreadID)
					return assistantResponse, nil
				}
			}
			return "", fmt.Errorf("no assistant message found")
		}
	}
}

func (tm *ThreadManager) GetRunStatus(runID string) (string, error) {
	url := fmt.Sprintf("https://api.openai.com/v1/threads/%s/runs/%s", tm.ThreadID, runID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get run status: %s", string(bodyBytes))
	}

	var runStatus struct {
		Status string `json:"status"`
	}
	err = json.NewDecoder(resp.Body).Decode(&runStatus)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	return runStatus.Status, nil
}

func (tm *ThreadManager) GetThreadMessages() ([]Message, error) {
	url := fmt.Sprintf("https://api.openai.com/v1/threads/%s/messages", tm.ThreadID)

	// Log the retrieval request
	log.Printf("Fetching messages from thread with ID: %s", tm.ThreadID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Failed to create HTTP request for thread message retrieval: %v", err)
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", os.Getenv("OPENAI_API_KEY")))
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send request to get thread messages: %v", err)
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Printf("Failed to fetch thread messages. StatusCode: %d, Response: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("failed to get thread messages: %s", string(bodyBytes))
	}

	// Log the raw response body from OpenAI for debugging purposes
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %v", err)
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	log.Printf("Raw thread messages response: %s", string(bodyBytes))

	var messagesResp struct {
		Data []Message `json:"data"`
	}
	err = json.Unmarshal(bodyBytes, &messagesResp)
	if err != nil {
		log.Printf("Failed to decode thread messages response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	// Log successful message retrieval
	log.Printf("Successfully fetched %d messages from thread with ID: %s", len(messagesResp.Data), tm.ThreadID)
	return messagesResp.Data, nil
}

type TextContent struct {
	Value       string        `json:"value"`
	Annotations []interface{} `json:"annotations"` // You can adjust this depending on what the annotations are
}

type ContentFragment struct {
	Type string       `json:"type"`
	Text *TextContent `json:"text,omitempty"` // Only include text if it's of type text
	// You can include other content types here like image, video, etc.
}

type Message struct {
	ID      string            `json:"id"`
	Role    string            `json:"role"`
	Content []ContentFragment `json:"content"` // Content is now a list of fragments
}
