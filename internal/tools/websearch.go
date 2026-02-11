package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/joakimcarlsson/ai/tool"
)

var logger = slog.With("tool", "web_search")

type WebSearchTool struct {
	httpClient *http.Client
	apiKey     string
}

func NewWebSearchTool(apiKey string) *WebSearchTool {
	return &WebSearchTool{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiKey: apiKey,
	}
}

type WebSearchParams struct {
	Query string `json:"query" desc:"The search query"`
}

type serpAPIResult struct {
	OrganicResults []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic_results"`
}

func (w *WebSearchTool) Info() tool.ToolInfo {
	return tool.NewToolInfo(
		"web_search",
		"Search the web for current information. Use this when you need to look up facts, news, or any real-time information.",
		WebSearchParams{},
	)
}

func (w *WebSearchTool) Run(ctx context.Context, params tool.ToolCall) (tool.ToolResponse, error) {
	if w.apiKey == "" {
		logger.Warn("api key not set")
		return tool.NewTextErrorResponse("Web search unavailable (SERPAPI_KEY not set)"), nil
	}

	var searchParams WebSearchParams
	if err := json.Unmarshal([]byte(params.Input), &searchParams); err != nil {
		logger.Error("invalid parameters", "error", err)
		return tool.NewTextErrorResponse("Invalid parameters: " + err.Error()), nil
	}

	logger.Info("searching", "query", searchParams.Query)

	apiURL, err := url.Parse("https://serpapi.com/search")
	if err != nil {
		logger.Error("building url", "error", err)
		return tool.NewTextErrorResponse("Failed to build URL: " + err.Error()), nil
	}

	query := apiURL.Query()
	query.Set("engine", "google")
	query.Set("q", searchParams.Query)
	query.Set("api_key", w.apiKey)
	query.Set("num", "5")
	apiURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL.String(), nil)
	if err != nil {
		logger.Error("creating request", "error", err)
		return tool.NewTextErrorResponse("Failed to create request: " + err.Error()), nil
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		logger.Error("executing request", "error", err)
		return tool.NewTextErrorResponse("Failed to search web: " + err.Error()), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("bad status", "status", resp.StatusCode)
		return tool.NewTextErrorResponse(fmt.Sprintf("Search API returned status %d", resp.StatusCode)), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("reading response", "error", err)
		return tool.NewTextErrorResponse("Failed to read response: " + err.Error()), nil
	}

	var result serpAPIResult
	if err := json.Unmarshal(body, &result); err != nil {
		logger.Error("parsing response", "error", err)
		return tool.NewTextErrorResponse("Failed to parse response: " + err.Error()), nil
	}

	if len(result.OrganicResults) == 0 {
		logger.Info("no results", "query", searchParams.Query)
		return tool.NewTextResponse(fmt.Sprintf("No results found for '%s'", searchParams.Query)), nil
	}

	logger.Info("results found", "query", searchParams.Query, "count", len(result.OrganicResults))

	output := fmt.Sprintf("Web search results for '%s':\n\n", searchParams.Query)
	for i, item := range result.OrganicResults {
		output += fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, item.Title, item.Snippet, item.Link)
	}

	return tool.NewTextResponse(output), nil
}
