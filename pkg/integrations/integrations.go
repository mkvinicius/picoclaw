// Package integrations provides connectors for cloud productivity services.
// Supports: Notion, Google Drive, Google Sheets, OneDrive/SharePoint.
// All operations are authenticated via OAuth2 tokens stored in the encrypted vault.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// ServiceType identifies the integration service.
type ServiceType string

const (
	ServiceNotion    ServiceType = "notion"
	ServiceGDrive    ServiceType = "google_drive"
	ServiceGSheets   ServiceType = "google_sheets"
	ServiceOneDrive  ServiceType = "onedrive"
)

// Credentials holds authentication tokens for a service.
type Credentials struct {
	Service      ServiceType `json:"service"`
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time   `json:"expires_at,omitempty"`
	WorkspaceID  string      `json:"workspace_id,omitempty"` // Notion
	Extra        map[string]string `json:"extra,omitempty"`
}

// Document represents a generic document from any service.
type Document struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Content   string            `json:"content"`
	URL       string            `json:"url"`
	Service   ServiceType       `json:"service"`
	UpdatedAt time.Time         `json:"updated_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// SpreadsheetData represents data from a spreadsheet.
type SpreadsheetData struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Sheets []Sheet    `json:"sheets"`
}

// Sheet represents a single sheet/tab in a spreadsheet.
type Sheet struct {
	Name   string     `json:"name"`
	Values [][]string `json:"values"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Integration Manager
// ─────────────────────────────────────────────────────────────────────────────

// Manager orchestrates all cloud integrations.
type Manager struct {
	creds  map[ServiceType]*Credentials
	client *http.Client
}

// NewManager creates a new integration manager.
func NewManager() *Manager {
	return &Manager{
		creds: make(map[ServiceType]*Credentials),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Register adds credentials for a service.
func (m *Manager) Register(creds Credentials) {
	m.creds[creds.Service] = &creds
	fmt.Printf("🔗 Integração registrada: %s\n", creds.Service)
}

// IsConnected checks if a service is configured.
func (m *Manager) IsConnected(service ServiceType) bool {
	c, ok := m.creds[service]
	return ok && c.AccessToken != ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Notion Integration
// ─────────────────────────────────────────────────────────────────────────────

const notionBase = "https://api.notion.com/v1"
const notionVersion = "2022-06-28"

// NotionSearch searches for pages and databases in Notion.
func (m *Manager) NotionSearch(ctx context.Context, query string) ([]Document, error) {
	creds, err := m.getCreds(ServiceNotion)
	if err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"query": query,
		"filter": map[string]string{"value": "page", "property": "object"},
	}

	resp, err := m.doRequest(ctx, "POST", notionBase+"/search", creds.AccessToken, body, map[string]string{
		"Notion-Version": notionVersion,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		Results []struct {
			ID         string `json:"id"`
			URL        string `json:"url"`
			LastEdited time.Time `json:"last_edited_time"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	docs := make([]Document, 0, len(result.Results))
	for _, r := range result.Results {
		title := extractNotionTitle(r.Properties)
		docs = append(docs, Document{
			ID:        r.ID,
			Title:     title,
			URL:       r.URL,
			Service:   ServiceNotion,
			UpdatedAt: r.LastEdited,
		})
	}
	return docs, nil
}

// NotionReadPage reads the content of a Notion page.
func (m *Manager) NotionReadPage(ctx context.Context, pageID string) (*Document, error) {
	creds, err := m.getCreds(ServiceNotion)
	if err != nil {
		return nil, err
	}

	// Get page metadata
	resp, err := m.doRequest(ctx, "GET", notionBase+"/pages/"+pageID, creds.AccessToken, nil, map[string]string{
		"Notion-Version": notionVersion,
	})
	if err != nil {
		return nil, err
	}

	var page struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	json.Unmarshal(resp, &page)

	// Get blocks (content)
	blocksResp, err := m.doRequest(ctx, "GET", notionBase+"/blocks/"+pageID+"/children", creds.AccessToken, nil, map[string]string{
		"Notion-Version": notionVersion,
	})
	if err != nil {
		return nil, err
	}

	var blocks struct {
		Results []struct {
			Type      string          `json:"type"`
			Paragraph json.RawMessage `json:"paragraph"`
			Heading1  json.RawMessage `json:"heading_1"`
			Heading2  json.RawMessage `json:"heading_2"`
		} `json:"results"`
	}
	json.Unmarshal(blocksResp, &blocks)

	content := extractNotionContent(blocks.Results)
	title := extractNotionTitle(page.Properties)

	return &Document{
		ID:      page.ID,
		Title:   title,
		Content: content,
		URL:     page.URL,
		Service: ServiceNotion,
	}, nil
}

// NotionCreatePage creates a new page in Notion.
func (m *Manager) NotionCreatePage(ctx context.Context, parentID, title, content string) (*Document, error) {
	creds, err := m.getCreds(ServiceNotion)
	if err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"parent": map[string]string{"page_id": parentID},
		"properties": map[string]interface{}{
			"title": []map[string]interface{}{
				{"text": map[string]string{"content": title}},
			},
		},
		"children": notionTextBlocks(content),
	}

	resp, err := m.doRequest(ctx, "POST", notionBase+"/pages", creds.AccessToken, body, map[string]string{
		"Notion-Version": notionVersion,
	})
	if err != nil {
		return nil, err
	}

	var page struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	json.Unmarshal(resp, &page)

	return &Document{
		ID:      page.ID,
		Title:   title,
		Content: content,
		URL:     page.URL,
		Service: ServiceNotion,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Google Drive Integration
// ─────────────────────────────────────────────────────────────────────────────

const driveBase = "https://www.googleapis.com/drive/v3"

// DriveSearch searches for files in Google Drive.
func (m *Manager) DriveSearch(ctx context.Context, query string) ([]Document, error) {
	creds, err := m.getCreds(ServiceGDrive)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("q", fmt.Sprintf("name contains '%s' and trashed = false", query))
	params.Set("fields", "files(id,name,webViewLink,modifiedTime,mimeType)")

	resp, err := m.doRequest(ctx, "GET", driveBase+"/files?"+params.Encode(), creds.AccessToken, nil, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Files []struct {
			ID           string    `json:"id"`
			Name         string    `json:"name"`
			WebViewLink  string    `json:"webViewLink"`
			ModifiedTime time.Time `json:"modifiedTime"`
			MimeType     string    `json:"mimeType"`
		} `json:"files"`
	}
	json.Unmarshal(resp, &result)

	docs := make([]Document, 0, len(result.Files))
	for _, f := range result.Files {
		docs = append(docs, Document{
			ID:        f.ID,
			Title:     f.Name,
			URL:       f.WebViewLink,
			Service:   ServiceGDrive,
			UpdatedAt: f.ModifiedTime,
			Metadata:  map[string]string{"mime_type": f.MimeType},
		})
	}
	return docs, nil
}

// DriveUpload uploads a file to Google Drive.
func (m *Manager) DriveUpload(ctx context.Context, name, content, mimeType string) (*Document, error) {
	creds, err := m.getCreds(ServiceGDrive)
	if err != nil {
		return nil, err
	}

	// Multipart upload
	metadata := map[string]string{"name": name}
	metaBytes, _ := json.Marshal(metadata)

	boundary := "boundary_picoclaw"
	body := fmt.Sprintf("--%s\r\nContent-Type: application/json\r\n\r\n%s\r\n--%s\r\nContent-Type: %s\r\n\r\n%s\r\n--%s--",
		boundary, string(metaBytes), boundary, mimeType, content, boundary)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart",
		strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var file struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		WebViewLink string `json:"webViewLink"`
	}
	json.Unmarshal(respBody, &file)

	return &Document{
		ID:      file.ID,
		Title:   file.Name,
		URL:     file.WebViewLink,
		Service: ServiceGDrive,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Google Sheets Integration
// ─────────────────────────────────────────────────────────────────────────────

const sheetsBase = "https://sheets.googleapis.com/v4/spreadsheets"

// SheetsRead reads data from a Google Sheets spreadsheet.
func (m *Manager) SheetsRead(ctx context.Context, spreadsheetID, rangeStr string) (*SpreadsheetData, error) {
	creds, err := m.getCreds(ServiceGSheets)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/%s/values/%s", sheetsBase, spreadsheetID, url.PathEscape(rangeStr))
	resp, err := m.doRequest(ctx, "GET", endpoint, creds.AccessToken, nil, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Range  string     `json:"range"`
		Values [][]string `json:"values"`
	}
	json.Unmarshal(resp, &result)

	return &SpreadsheetData{
		ID: spreadsheetID,
		Sheets: []Sheet{{
			Name:   result.Range,
			Values: result.Values,
		}},
	}, nil
}

// SheetsWrite writes data to a Google Sheets spreadsheet.
func (m *Manager) SheetsWrite(ctx context.Context, spreadsheetID, rangeStr string, values [][]interface{}) error {
	creds, err := m.getCreds(ServiceGSheets)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"range":          rangeStr,
		"majorDimension": "ROWS",
		"values":         values,
	}

	endpoint := fmt.Sprintf("%s/%s/values/%s?valueInputOption=USER_ENTERED",
		sheetsBase, spreadsheetID, url.PathEscape(rangeStr))
	_, err = m.doRequest(ctx, "PUT", endpoint, creds.AccessToken, body, nil)
	return err
}

// SheetsAppend appends rows to a Google Sheets spreadsheet.
func (m *Manager) SheetsAppend(ctx context.Context, spreadsheetID, rangeStr string, values [][]interface{}) error {
	creds, err := m.getCreds(ServiceGSheets)
	if err != nil {
		return err
	}

	body := map[string]interface{}{
		"range":          rangeStr,
		"majorDimension": "ROWS",
		"values":         values,
	}

	endpoint := fmt.Sprintf("%s/%s/values/%s:append?valueInputOption=USER_ENTERED&insertDataOption=INSERT_ROWS",
		sheetsBase, spreadsheetID, url.PathEscape(rangeStr))
	_, err = m.doRequest(ctx, "POST", endpoint, creds.AccessToken, body, nil)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// OneDrive Integration
// ─────────────────────────────────────────────────────────────────────────────

const oneDriveBase = "https://graph.microsoft.com/v1.0/me/drive"

// OneDriveSearch searches for files in OneDrive.
func (m *Manager) OneDriveSearch(ctx context.Context, query string) ([]Document, error) {
	creds, err := m.getCreds(ServiceOneDrive)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/root/search(q='%s')", oneDriveBase, url.QueryEscape(query))
	resp, err := m.doRequest(ctx, "GET", endpoint, creds.AccessToken, nil, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Value []struct {
			ID                 string    `json:"id"`
			Name               string    `json:"name"`
			WebURL             string    `json:"webUrl"`
			LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
		} `json:"value"`
	}
	json.Unmarshal(resp, &result)

	docs := make([]Document, 0, len(result.Value))
	for _, f := range result.Value {
		docs = append(docs, Document{
			ID:        f.ID,
			Title:     f.Name,
			URL:       f.WebURL,
			Service:   ServiceOneDrive,
			UpdatedAt: f.LastModifiedDateTime,
		})
	}
	return docs, nil
}

// OneDriveUpload uploads a file to OneDrive.
func (m *Manager) OneDriveUpload(ctx context.Context, name, content string) (*Document, error) {
	creds, err := m.getCreds(ServiceOneDrive)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/root:/%s:/content", oneDriveBase, url.PathEscape(name))
	req, _ := http.NewRequestWithContext(ctx, "PUT", endpoint, strings.NewReader(content))
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var file struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		WebURL string `json:"webUrl"`
	}
	json.Unmarshal(respBody, &file)

	return &Document{
		ID:      file.ID,
		Title:   file.Name,
		URL:     file.WebURL,
		Service: ServiceOneDrive,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Universal Search (searches all connected services)
// ─────────────────────────────────────────────────────────────────────────────

// SearchAll searches across all connected services simultaneously.
func (m *Manager) SearchAll(ctx context.Context, query string) map[ServiceType][]Document {
	results := make(map[ServiceType][]Document)

	if m.IsConnected(ServiceNotion) {
		if docs, err := m.NotionSearch(ctx, query); err == nil {
			results[ServiceNotion] = docs
		}
	}
	if m.IsConnected(ServiceGDrive) {
		if docs, err := m.DriveSearch(ctx, query); err == nil {
			results[ServiceGDrive] = docs
		}
	}
	if m.IsConnected(ServiceOneDrive) {
		if docs, err := m.OneDriveSearch(ctx, query); err == nil {
			results[ServiceOneDrive] = docs
		}
	}

	return results
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (m *Manager) getCreds(service ServiceType) (*Credentials, error) {
	c, ok := m.creds[service]
	if !ok || c.AccessToken == "" {
		return nil, fmt.Errorf("serviço não configurado: %s. Use 'picoclaw integrations connect %s'", service, service)
	}
	return c, nil
}

func (m *Manager) doRequest(ctx context.Context, method, endpoint, token string, body interface{}, extraHeaders map[string]string) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func extractNotionTitle(props map[string]json.RawMessage) string {
	for _, v := range props {
		var prop struct {
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		}
		if json.Unmarshal(v, &prop) == nil && len(prop.Title) > 0 {
			return prop.Title[0].PlainText
		}
	}
	return "Sem título"
}

func extractNotionContent(blocks []struct {
	Type      string          `json:"type"`
	Paragraph json.RawMessage `json:"paragraph"`
	Heading1  json.RawMessage `json:"heading_1"`
	Heading2  json.RawMessage `json:"heading_2"`
}) string {
	var sb strings.Builder
	for _, b := range blocks {
		var richText struct {
			RichText []struct {
				PlainText string `json:"plain_text"`
			} `json:"rich_text"`
		}
		switch b.Type {
		case "paragraph":
			json.Unmarshal(b.Paragraph, &richText)
		case "heading_1":
			json.Unmarshal(b.Heading1, &richText)
			sb.WriteString("# ")
		case "heading_2":
			json.Unmarshal(b.Heading2, &richText)
			sb.WriteString("## ")
		}
		for _, rt := range richText.RichText {
			sb.WriteString(rt.PlainText)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func notionTextBlocks(content string) []map[string]interface{} {
	lines := strings.Split(content, "\n")
	blocks := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		blocks = append(blocks, map[string]interface{}{
			"object": "block",
			"type":   "paragraph",
			"paragraph": map[string]interface{}{
				"rich_text": []map[string]interface{}{
					{"type": "text", "text": map[string]string{"content": line}},
				},
			},
		})
	}
	return blocks
}
