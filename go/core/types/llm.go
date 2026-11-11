package types

// ChatRole is who wrote a ChatMessage.
type ChatRole string

const (
	// RoleUser is a message from the person or caller asking.
	RoleUser ChatRole = "user"

	// RoleAssistant is an earlier reply from the model.
	RoleAssistant ChatRole = "assistant"
)

// ChatMessage is one turn of a conversation sent to a model.
type ChatMessage struct {
	// Role is who wrote the turn.
	Role ChatRole

	// Content is the turn's text.
	Content string
}

// CompletionRequest is one chat completion asked of a model.
type CompletionRequest struct {
	// System is the system prompt; empty sends none.
	System string

	// Model names the model; empty uses the client's configured default.
	Model string

	// Messages are the conversation's turns, oldest first.
	Messages []ChatMessage

	// Temperature is the sampling temperature; zero is the most deterministic.
	Temperature float64

	// MaxOutputTokens caps the reply's tokens; zero leaves the backend's default.
	MaxOutputTokens int
}

// Completion is a model's reply and what it cost.
type Completion struct {
	// Text is the reply.
	Text string

	// Model is the model that answered.
	Model string

	// InputTokens is the prompt's token count, as the backend reports it.
	InputTokens int

	// OutputTokens is the reply's token count, as the backend reports it.
	OutputTokens int
}
