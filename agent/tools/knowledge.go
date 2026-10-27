package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// KnowledgeSearch is a RAG tool that searches the ~/.ollama/knowledge/ catalog.
//
// Tool description (what the model sees):
//
//	"Search loaded knowledge base documents for relevant information.
//	 Use retrieved fragments as factual grounding for your answer, but keep a
//	 natural conversational tone — do not quote sources verbatim. If no
//	 relevant results are found, say so briefly and answer from your own
//	 knowledge."
//
// Why this specific wording: after RAG search, models tend to switch to
// flat "citation mode" (dry answers). Explicit instruction to keep natural
// tone + limit on chunk count (≤3) mitigates this.
type KnowledgeSearch struct{}

func (KnowledgeSearch) Name() string { return "knowledge_search" }

func (KnowledgeSearch) Description() string {
	return "Search the loaded knowledge base for relevant information about a topic."
}

func (KnowledgeSearch) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("query", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "What to search for in the knowledge base.",
	})
	return api.ToolFunction{
		Name:        "knowledge_search",
		Description: (KnowledgeSearch{}).Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"query"},
		},
	}
}

func (KnowledgeSearch) Execute(_ context.Context, _ agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return agent.ToolResult{Content: "Error: query is empty"}, nil
	}

	// TODO: implement BM25 search via SQLite FTS5.
	// Steps when implementing:
	//  1. Open ~/.ollama/knowledge/.index.db (create if missing).
	//  2. Query FTS5 table with the user's query string.
	//  3. Return top-3 chunks (each ≤500 words) as formatted text.
	//  4. If index is empty or no matches → "Knowledge base is empty or no matches found."
	//
	// Future: hybrid search with embedding vectors (sqlite-vec).

	return agent.ToolResult{Content: fmt.Sprintf(
		"[knowledge_search stub] query=%q — implementation pending. "+
			"Place .txt/.md files in ~/.ollama/knowledge/ and re-run after indexing.",
		query,
	)}, nil
}
