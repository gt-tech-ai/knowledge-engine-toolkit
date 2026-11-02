package llm

// Mockgen directive for the injectable Bedrock Converse seam (Deps.Converse), so
// black-box tests drive the bedrock kind through a generated mock. The destination is
// go/tests/mocks (two levels up).
//go:generate mockgen -destination=../../tests/mocks/mock_llm_converse.go -package=mocks -mock_names=ConverseAPI=MockLLMConverseAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm/bedrock ConverseAPI
