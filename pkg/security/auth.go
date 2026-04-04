// auth.go — Role-based access control for PicoClaw home automation.
//
// Each sender (identified by their canonical ID from SenderInfo) is assigned
// a Role that determines which tool categories they can invoke. This sits
// on top of the existing channel-level allow_from filter: allow_from controls
// who can speak at all; roles control what they can do.
//
// # Roles (least → most privileged)
//
//	Guest   – read-only: weather, time, status queries
//	User    – standard: everything Guest + messaging, web search, schedules
//	Admin   – privileged: everything User + exec, file write, camera, email
//	Owner   – unrestricted: all tools, including system-level operations
//
// # Configuration (config.json)
//
//	"security": {
//	  "roles": {
//	    "owner":  ["telegram:123456789"],
//	    "admin":  ["telegram:987654321", "@alice"],
//	    "user":   ["discord:111222333"],
//	    "guest":  []          // ← empty = all allow_from senders get Guest
//	  },
//	  "default_role": "guest"   // role for senders not in any list
//	}
//
// Entries use the same format as allow_from: "platform:id", "@username",
// or bare "id".
package security

import (
	"strings"
	"sync"
)

// Role represents a user's privilege level.
type Role string

const (
	RoleGuest Role = "guest"
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
	RoleOwner Role = "owner"
)

// roleRank maps roles to a numeric rank for comparison.
var roleRank = map[Role]int{
	RoleGuest: 0,
	RoleUser:  1,
	RoleAdmin: 2,
	RoleOwner: 3,
}

// AtLeast returns true if r has at least the privilege level of minimum.
func (r Role) AtLeast(minimum Role) bool {
	return roleRank[r] >= roleRank[minimum]
}

// ToolCategory groups tools by required privilege level.
type ToolCategory string

const (
	// CategoryReadOnly: status, weather, time, web search (read)
	CategoryReadOnly ToolCategory = "read_only"
	// CategoryMessaging: send messages, reminders, schedules
	CategoryMessaging ToolCategory = "messaging"
	// CategoryFileRead: read files within workspace
	CategoryFileRead ToolCategory = "file_read"
	// CategoryFileWrite: write/edit/append files
	CategoryFileWrite ToolCategory = "file_write"
	// CategoryExec: run shell commands
	CategoryExec ToolCategory = "exec"
	// CategoryCamera: access camera feeds and motion events
	CategoryCamera ToolCategory = "camera"
	// CategoryEmail: send and read email
	CategoryEmail ToolCategory = "email"
	// CategorySystem: fleet management, config reload, hardware GPIO
	CategorySystem ToolCategory = "system"
)

// toolCategoryMinRole defines the minimum role required for each tool category.
var toolCategoryMinRole = map[ToolCategory]Role{
	CategoryReadOnly:  RoleGuest,
	CategoryMessaging: RoleUser,
	CategoryFileRead:  RoleUser,
	CategoryFileWrite: RoleAdmin,
	CategoryExec:      RoleAdmin,
	CategoryCamera:    RoleAdmin,
	CategoryEmail:     RoleAdmin,
	CategorySystem:    RoleOwner,
}

// toolNameCategory maps tool names (as registered in ToolRegistry) to their
// category. Tools not listed here default to CategoryExec (most restrictive).
var toolNameCategory = map[string]ToolCategory{
	"web_search":   CategoryReadOnly,
	"web_fetch":    CategoryReadOnly,
	"read_file":    CategoryFileRead,
	"list_dir":     CategoryFileRead,
	"write_file":   CategoryFileWrite,
	"edit_file":    CategoryFileWrite,
	"append_file":  CategoryFileWrite,
	"exec":         CategoryExec,
	"message":      CategoryMessaging,
	"cron":         CategoryMessaging,
	"schedule":     CategoryMessaging,
	"spawn":        CategoryAdmin,
	"skills":       CategoryAdmin,
	"i2c":          CategorySystem,
	"spi":          CategorySystem,
	"gpio":         CategorySystem,
	"fleet":        CategorySystem,
}

// CategoryAdmin is a helper alias used in toolNameCategory.
const CategoryAdmin ToolCategory = "admin"

func init() {
	toolCategoryMinRole[CategoryAdmin] = RoleAdmin
}

// RoleConfig holds the role assignment lists from config.
type RoleConfig struct {
	// OwnerIDs, AdminIDs, UserIDs, GuestIDs are lists of canonical sender IDs.
	// Same format as allow_from: "platform:id", "@username", or bare "id".
	OwnerIDs   []string `json:"owner"`
	AdminIDs   []string `json:"admin"`
	UserIDs    []string `json:"user"`
	GuestIDs   []string `json:"guest"`
	// DefaultRole is used for senders not in any list (default: "guest").
	DefaultRole Role `json:"default_role"`
}

// Authorizer resolves a sender's Role and checks tool permissions.
type Authorizer struct {
	mu     sync.RWMutex
	owners map[string]bool
	admins map[string]bool
	users  map[string]bool
	guests map[string]bool
	def    Role
}

// NewAuthorizer creates an Authorizer from a RoleConfig.
func NewAuthorizer(cfg RoleConfig) *Authorizer {
	def := cfg.DefaultRole
	if def == "" {
		def = RoleGuest
	}
	return &Authorizer{
		owners: toSet(cfg.OwnerIDs),
		admins: toSet(cfg.AdminIDs),
		users:  toSet(cfg.UserIDs),
		guests: toSet(cfg.GuestIDs),
		def:    def,
	}
}

// DefaultAuthorizer returns an Authorizer that grants Owner to everyone.
// Used when no role config is provided (backwards-compatible default).
func DefaultAuthorizer() *Authorizer {
	return &Authorizer{def: RoleOwner}
}

// RoleOf returns the highest Role assigned to the given sender.
// senderID should be the canonical ID (e.g. "telegram:123456789").
func (a *Authorizer) RoleOf(senderID string) Role {
	if senderID == "" {
		return a.def
	}
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Check in priority order (highest privilege first).
	if a.matchesAny(senderID, a.owners) {
		return RoleOwner
	}
	if a.matchesAny(senderID, a.admins) {
		return RoleAdmin
	}
	if a.matchesAny(senderID, a.users) {
		return RoleUser
	}
	if a.matchesAny(senderID, a.guests) {
		return RoleGuest
	}
	return a.def
}

// CanUseTool returns true if the sender is allowed to invoke the named tool.
func (a *Authorizer) CanUseTool(senderID, toolName string) bool {
	role := a.RoleOf(senderID)
	cat, ok := toolNameCategory[toolName]
	if !ok {
		// Unknown tools require Admin by default.
		cat = CategoryExec
	}
	minRole, ok := toolCategoryMinRole[cat]
	if !ok {
		minRole = RoleAdmin
	}
	return role.AtLeast(minRole)
}

// CanSendMessage returns true if the sender has at least Guest role
// (i.e. is not completely barred from the system).
func (a *Authorizer) CanSendMessage(senderID string) bool {
	return a.RoleOf(senderID).AtLeast(RoleGuest)
}

// Reload atomically replaces the role lists.
func (a *Authorizer) Reload(cfg RoleConfig) {
	def := cfg.DefaultRole
	if def == "" {
		def = RoleGuest
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.owners = toSet(cfg.OwnerIDs)
	a.admins = toSet(cfg.AdminIDs)
	a.users = toSet(cfg.UserIDs)
	a.guests = toSet(cfg.GuestIDs)
	a.def = def
}

// matchesAny checks if senderID matches any entry in the set.
// Matching is flexible: supports canonical "platform:id", "@username",
// bare numeric IDs, and case-insensitive usernames.
func (a *Authorizer) matchesAny(senderID string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	// Direct match first (fastest path).
	if set[senderID] {
		return true
	}
	// Normalised match: strip platform prefix and compare bare ID.
	if idx := strings.LastIndex(senderID, ":"); idx >= 0 {
		bare := senderID[idx+1:]
		if set[bare] {
			return true
		}
	}
	// Case-insensitive match for @usernames.
	lower := strings.ToLower(senderID)
	for entry := range set {
		if strings.ToLower(entry) == lower {
			return true
		}
	}
	return false
}

func toSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	return m
}
