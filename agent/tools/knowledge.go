package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
	knowledge "github.com/ollama/ollama/internal/knowledge"
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

	dir, err := knowledge.Dir()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: cannot open knowledge base: %v", err)}, nil
	}

	res, note, err := searchKnowledge(dir, query)
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: knowledge search failed: %v", err)}, nil
	}
	content := ""
	if note != "" {
		content = note + "\n\n"
	}
	content += res
	return agent.ToolResult{Content: content}, nil
}

// searchKnowledge runs a BM25 lookup over the knowledge base, transparently
// reindexing first when the index lags behind files on disk (lazy reindex).
func searchKnowledge(dir string, query string) (content string, note string, err error) {
	st, err := knowledge.IndexStatus(dir)
	if err != nil {
		return "", "", fmt.Errorf("check index status: %w", err)
	}

	if st.Files == 0 {
		return "Knowledge base is empty. Place .txt/.md files in ~/.ollama/knowledge/ and re-run the search.", "", nil
	}

	if st.Stale {
		if _, err := knowledge.Index(dir); err != nil {
			return "", "", fmt.Errorf("reindex: %w", err)
		}
		note = "Knowledge base was just (re)indexed."
	}

	results, err := knowledge.Search(dir, query, knowledge.MaxChunks)
	if err != nil {
		return "", note, fmt.Errorf("search: %w", err)
	}
	if len(results) == 0 {
		return "No relevant results found in the knowledge base for this query.", note, nil
	}

	var b strings.Builder
	// Deduplicate adjacent chunks from the same file: keep only the best hit.
	kept, hidden := 0, 0
	fileCache := map[string][]byte{}
	for i := range results {
		r := &results[i]
		if kept>0 && r.Path == results[i-1].Path && r.Offset-results[i-1].Offset < 4000 {
			hidden++
			continue
		}
		abs := filepath.Join(dir, r.Path)
		startL, endL := lineRange(fileCache, abs, r.Offset, r.Content)
		if kept > 0 {
			b.WriteString("\n")
		}
		heading := ""
		if r.Heading != "" {
			heading = " § " + r.Heading
		}
		fmt.Fprintf(&b, "--- Source: %s%s (lines %d-%d) ---\n%s", abs, heading, startL, endL, r.Content)
		kept++
	}
	head := fmt.Sprintf("Found %d relevant fragment(s):\n\n", kept)
	if hidden > 0 {
		head = fmt.Sprintf("Found %d relevant fragment(s) (%d adjacent duplicate(s) omitted):\n\n", kept, hidden)
	}
	return head + b.String(), note, nil
}

// lineRange converts a chunk's byte offset into an inclusive 1-based line
// range (startLine-endLine) so the agent can fetch it directly with the read
// tool: `read <absPath> start=startLine end=endLine`. The file is read once per
// unique path and cached for the duration of one search call.
func lineRange(cache map[string][]byte, abs string, offset int, content string) (int, int) {
	var data []byte
	if b, ok := cache[abs]; ok {
		data = b
	} else if b, err := os.ReadFile(abs); err == nil {
		cache[abs] = b
		data = b
	}
	n := len(data)
	if offset > n {
		offset = n
	}
	startLine := strings.Count(string(data[:offset]), "\n") + 1
	endLine := startLine + strings.Count(content, "\n")
	if endLine < startLine {
		endLine = startLine
	}
	return startLine, endLine
}
