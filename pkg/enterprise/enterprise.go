// Package enterprise provides multi-user enterprise mode for PicoClaw.
// Features: Role-Based Access Control (RBAC), centralized audit logging,
// shared squads, user management, and team collaboration.
package enterprise

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// Role defines a user role with associated permissions.
type Role string

const (
	RoleOwner   Role = "owner"   // full access, billing, user management
	RoleAdmin   Role = "admin"   // manage squads, users (except owner)
	RoleManager Role = "manager" // manage squads, view all activity
	RoleUser    Role = "user"    // run squads, view own activity
	RoleViewer  Role = "viewer"  // read-only access
)

// Permission defines a specific capability.
type Permission string

const (
	PermManageUsers     Permission = "manage_users"
	PermManageSquads    Permission = "manage_squads"
	PermRunSquads       Permission = "run_squads"
	PermViewAuditLog    Permission = "view_audit_log"
	PermManageIntegrations Permission = "manage_integrations"
	PermViewMetrics     Permission = "view_metrics"
	PermManageBilling   Permission = "manage_billing"
	PermExportData      Permission = "export_data"
	PermManageSecurity  Permission = "manage_security"
)

// rolePermissions maps roles to their default permissions.
var rolePermissions = map[Role][]Permission{
	RoleOwner: {
		PermManageUsers, PermManageSquads, PermRunSquads,
		PermViewAuditLog, PermManageIntegrations, PermViewMetrics,
		PermManageBilling, PermExportData, PermManageSecurity,
	},
	RoleAdmin: {
		PermManageUsers, PermManageSquads, PermRunSquads,
		PermViewAuditLog, PermManageIntegrations, PermViewMetrics,
		PermExportData, PermManageSecurity,
	},
	RoleManager: {
		PermManageSquads, PermRunSquads,
		PermViewAuditLog, PermViewMetrics,
	},
	RoleUser: {
		PermRunSquads, PermViewMetrics,
	},
	RoleViewer: {
		PermViewMetrics,
	},
}

// User represents an enterprise user.
type User struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Email        string            `json:"email"`
	Role         Role              `json:"role"`
	Teams        []string          `json:"teams"`
	APIKey       string            `json:"api_key,omitempty"`
	PasswordHash string            `json:"password_hash,omitempty"`
	Active       bool              `json:"active"`
	CreatedAt    time.Time         `json:"created_at"`
	LastLogin    time.Time         `json:"last_login,omitempty"`
	Preferences  map[string]string `json:"preferences,omitempty"`
	CustomPerms  []Permission      `json:"custom_permissions,omitempty"` // extra perms beyond role
}

// Team represents a group of users sharing squads.
type Team struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Members     []string  `json:"members"` // user IDs
	Squads      []string  `json:"squads"`  // squad IDs accessible to team
	CreatedAt   time.Time `json:"created_at"`
}

// AuditEntry records a user action for compliance.
type AuditEntry struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	UserName  string    `json:"user_name"`
	Action    string    `json:"action"`
	Resource  string    `json:"resource"`
	Details   string    `json:"details"`
	IP        string    `json:"ip,omitempty"`
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}

// Session represents an active user session.
type Session struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IP        string    `json:"ip,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Enterprise Manager
// ─────────────────────────────────────────────────────────────────────────────

// Manager handles enterprise multi-user operations.
type Manager struct {
	dataDir  string
	users    map[string]*User
	teams    map[string]*Team
	sessions map[string]*Session
	audit    []*AuditEntry
	mu       sync.RWMutex
	enabled  bool
}

// NewManager creates a new enterprise manager.
func NewManager(dataDir string) *Manager {
	return &Manager{
		dataDir:  dataDir,
		users:    make(map[string]*User),
		teams:    make(map[string]*Team),
		sessions: make(map[string]*Session),
	}
}

// Load loads enterprise data from disk.
func (m *Manager) Load() error {
	os.MkdirAll(m.dataDir, 0700)

	// Load users
	if data, err := os.ReadFile(filepath.Join(m.dataDir, "users.json")); err == nil {
		var users map[string]*User
		if json.Unmarshal(data, &users) == nil {
			m.mu.Lock()
			m.users = users
			m.enabled = len(users) > 0
			m.mu.Unlock()
		}
	}

	// Load teams
	if data, err := os.ReadFile(filepath.Join(m.dataDir, "teams.json")); err == nil {
		var teams map[string]*Team
		if json.Unmarshal(data, &teams) == nil {
			m.mu.Lock()
			m.teams = teams
			m.mu.Unlock()
		}
	}

	if m.enabled {
		fmt.Printf("🏢 Modo Empresarial: %d usuários, %d equipes\n", len(m.users), len(m.teams))
	}
	return nil
}

// Enable activates enterprise mode and creates the first owner.
func (m *Manager) Enable(ownerName, ownerEmail, password string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.enabled {
		return nil, fmt.Errorf("modo empresarial já está ativo")
	}

	owner := &User{
		ID:           generateID("usr"),
		Name:         ownerName,
		Email:        ownerEmail,
		Role:         RoleOwner,
		Active:       true,
		CreatedAt:    time.Now(),
		Preferences:  make(map[string]string),
		PasswordHash: hashPassword(password),
		APIKey:       generateAPIKey(),
	}

	m.users[owner.ID] = owner
	m.enabled = true

	m.saveUsers()
	fmt.Printf("🏢 Modo Empresarial ativado! Proprietário: %s (%s)\n", ownerName, ownerEmail)
	return owner, nil
}

// CreateUser creates a new enterprise user.
func (m *Manager) CreateUser(ctx context.Context, callerID, name, email string, role Role) (*User, error) {
	if err := m.requirePermission(callerID, PermManageUsers); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check email uniqueness
	for _, u := range m.users {
		if u.Email == email {
			return nil, fmt.Errorf("email já cadastrado: %s", email)
		}
	}

	user := &User{
		ID:          generateID("usr"),
		Name:        name,
		Email:       email,
		Role:        role,
		Active:      true,
		CreatedAt:   time.Now(),
		Preferences: make(map[string]string),
		APIKey:      generateAPIKey(),
	}

	m.users[user.ID] = user
	m.saveUsers()

	m.addAudit(callerID, "create_user", "user:"+user.ID,
		fmt.Sprintf("Criou usuário %s (%s) com papel %s", name, email, role), true)

	return user, nil
}

// Login authenticates a user and returns a session token.
func (m *Manager) Login(email, password string, ip string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var user *User
	for _, u := range m.users {
		if u.Email == email && u.Active {
			user = u
			break
		}
	}

	if user == nil {
		return nil, fmt.Errorf("usuário não encontrado")
	}

	if user.PasswordHash != hashPassword(password) {
		m.addAudit(user.ID, "login_failed", "session", "Senha incorreta", false)
		return nil, fmt.Errorf("senha incorreta")
	}

	session := &Session{
		Token:     generateToken(),
		UserID:    user.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
		IP:        ip,
	}

	m.sessions[session.Token] = session
	user.LastLogin = time.Now()
	m.saveUsers()

	m.addAudit(user.ID, "login", "session", "Login bem-sucedido", true)
	return session, nil
}

// LoginWithAPIKey authenticates using an API key.
func (m *Manager) LoginWithAPIKey(apiKey string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, u := range m.users {
		if u.APIKey == apiKey && u.Active {
			return u, nil
		}
	}
	return nil, fmt.Errorf("API key inválida")
}

// ValidateSession validates a session token and returns the user.
func (m *Manager) ValidateSession(token string) (*User, error) {
	m.mu.RLock()
	session, ok := m.sessions[token]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("sessão não encontrada")
	}
	if time.Now().After(session.ExpiresAt) {
		return nil, fmt.Errorf("sessão expirada")
	}

	m.mu.RLock()
	user, ok := m.users[session.UserID]
	m.mu.RUnlock()

	if !ok || !user.Active {
		return nil, fmt.Errorf("usuário inativo")
	}

	return user, nil
}

// HasPermission checks if a user has a specific permission.
func (m *Manager) HasPermission(userID string, perm Permission) bool {
	m.mu.RLock()
	user, ok := m.users[userID]
	m.mu.RUnlock()

	if !ok || !user.Active {
		return false
	}

	// Check role permissions
	for _, p := range rolePermissions[user.Role] {
		if p == perm {
			return true
		}
	}

	// Check custom permissions
	for _, p := range user.CustomPerms {
		if p == perm {
			return true
		}
	}

	return false
}

// CreateTeam creates a new team.
func (m *Manager) CreateTeam(ctx context.Context, callerID, name, description string) (*Team, error) {
	if err := m.requirePermission(callerID, PermManageSquads); err != nil {
		return nil, err
	}

	team := &Team{
		ID:          generateID("team"),
		Name:        name,
		Description: description,
		CreatedAt:   time.Now(),
	}

	m.mu.Lock()
	m.teams[team.ID] = team
	m.mu.Unlock()

	m.saveTeams()
	m.addAudit(callerID, "create_team", "team:"+team.ID, fmt.Sprintf("Criou equipe: %s", name), true)
	return team, nil
}

// AddTeamMember adds a user to a team.
func (m *Manager) AddTeamMember(ctx context.Context, callerID, teamID, userID string) error {
	if err := m.requirePermission(callerID, PermManageUsers); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	team, ok := m.teams[teamID]
	if !ok {
		return fmt.Errorf("equipe não encontrada: %s", teamID)
	}

	// Check if already member
	for _, id := range team.Members {
		if id == userID {
			return nil // already member
		}
	}

	team.Members = append(team.Members, userID)

	// Update user's teams
	if user, ok := m.users[userID]; ok {
		user.Teams = append(user.Teams, teamID)
	}

	m.saveTeams()
	m.saveUsers()
	return nil
}

// GetAuditLog returns audit entries with optional filtering.
func (m *Manager) GetAuditLog(callerID string, limit int, userFilter string) ([]AuditEntry, error) {
	if err := m.requirePermission(callerID, PermViewAuditLog); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var entries []AuditEntry
	for i := len(m.audit) - 1; i >= 0; i-- {
		e := m.audit[i]
		if userFilter != "" && e.UserID != userFilter {
			continue
		}
		entries = append(entries, *e)
		if limit > 0 && len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// ListUsers returns all users (requires manage_users permission).
func (m *Manager) ListUsers(callerID string) ([]User, error) {
	if err := m.requirePermission(callerID, PermManageUsers); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	users := make([]User, 0, len(m.users))
	for _, u := range m.users {
		// Don't expose sensitive fields
		safe := *u
		safe.PasswordHash = ""
		safe.APIKey = ""
		users = append(users, safe)
	}
	return users, nil
}

// DeactivateUser deactivates a user account.
func (m *Manager) DeactivateUser(ctx context.Context, callerID, targetID string) error {
	if err := m.requirePermission(callerID, PermManageUsers); err != nil {
		return err
	}

	m.mu.Lock()
	user, ok := m.users[targetID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("usuário não encontrado")
	}
	// Can't deactivate owner
	if user.Role == RoleOwner {
		m.mu.Unlock()
		return fmt.Errorf("não é possível desativar o proprietário")
	}
	user.Active = false
	m.mu.Unlock()

	m.saveUsers()
	m.addAudit(callerID, "deactivate_user", "user:"+targetID, "Usuário desativado", true)
	return nil
}

// IsEnabled returns whether enterprise mode is active.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (m *Manager) requirePermission(userID string, perm Permission) error {
	if !m.IsEnabled() {
		return nil // single-user mode: all operations allowed
	}
	if !m.HasPermission(userID, perm) {
		return fmt.Errorf("permissão negada: %s requer %s", userID, perm)
	}
	return nil
}

func (m *Manager) addAudit(userID, action, resource, details string, success bool) {
	userName := "unknown"
	m.mu.RLock()
	if u, ok := m.users[userID]; ok {
		userName = u.Name
	}
	m.mu.RUnlock()

	entry := &AuditEntry{
		ID:        generateID("aud"),
		UserID:    userID,
		UserName:  userName,
		Action:    action,
		Resource:  resource,
		Details:   details,
		Success:   success,
		Timestamp: time.Now(),
	}

	m.mu.Lock()
	m.audit = append(m.audit, entry)
	// Keep last 10000 entries in memory
	if len(m.audit) > 10000 {
		m.audit = m.audit[len(m.audit)-10000:]
	}
	m.mu.Unlock()

	go m.appendAuditToDisk(entry)
}

func (m *Manager) appendAuditToDisk(entry *AuditEntry) {
	path := filepath.Join(m.dataDir, "audit.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(entry)
	f.Write(append(data, '\n'))
}

func (m *Manager) saveUsers() {
	data, err := json.MarshalIndent(m.users, "", "  ")
	if err == nil {
		os.WriteFile(filepath.Join(m.dataDir, "users.json"), data, 0600)
	}
}

func (m *Manager) saveTeams() {
	data, err := json.MarshalIndent(m.teams, "", "  ")
	if err == nil {
		os.WriteFile(filepath.Join(m.dataDir, "teams.json"), data, 0600)
	}
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateAPIKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "pc_" + hex.EncodeToString(b)
}

func hashPassword(password string) string {
	h := sha256.Sum256([]byte(password + ":picoclaw_salt_v1"))
	return hex.EncodeToString(h[:])
}

// RoleDescription returns a human-readable description of a role.
func RoleDescription(role Role) string {
	descriptions := map[Role]string{
		RoleOwner:   "Proprietário: acesso total, gerencia faturamento e usuários",
		RoleAdmin:   "Administrador: gerencia squads, usuários e integrações",
		RoleManager: "Gerente: gerencia squads e visualiza todas as atividades",
		RoleUser:    "Usuário: executa squads e visualiza suas próprias atividades",
		RoleViewer:  "Visualizador: acesso somente leitura",
	}
	if desc, ok := descriptions[role]; ok {
		return desc
	}
	return string(role)
}

// PermissionList returns all permissions for a role as strings.
func PermissionList(role Role) []string {
	perms := rolePermissions[role]
	result := make([]string, len(perms))
	for i, p := range perms {
		result[i] = string(p)
	}
	return result
}

// ParseRole parses a role string.
func ParseRole(s string) (Role, error) {
	roles := map[string]Role{
		"owner": RoleOwner, "admin": RoleAdmin,
		"manager": RoleManager, "user": RoleUser, "viewer": RoleViewer,
		"proprietário": RoleOwner, "administrador": RoleAdmin,
		"gerente": RoleManager, "usuário": RoleUser, "visualizador": RoleViewer,
	}
	if r, ok := roles[strings.ToLower(s)]; ok {
		return r, nil
	}
	return "", fmt.Errorf("papel inválido: %s", s)
}
