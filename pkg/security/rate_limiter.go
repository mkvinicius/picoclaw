package security

import (
	"fmt"
	"sync"
	"time"
)

// RateLimitConfig holds the configuration for the RateLimiter.
type RateLimitConfig struct {
	// MaxRequestsPerMinute is the maximum number of requests allowed per session per minute.
	MaxRequestsPerMinute int
	// MaxRequestsPerHour is the maximum number of requests allowed per session per hour.
	MaxRequestsPerHour int
	// MaxCommandsPerMinute is the maximum number of shell commands per session per minute.
	MaxCommandsPerMinute int
	// BanDurationSeconds is how long a session is banned after exceeding limits.
	BanDurationSeconds int
	// MaxBanStrikes is the number of bans before a session is permanently blocked.
	MaxBanStrikes int
}

// DefaultRateLimitConfig returns a secure default rate limit configuration.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		MaxRequestsPerMinute: 60,
		MaxRequestsPerHour:   500,
		MaxCommandsPerMinute: 20,
		BanDurationSeconds:   300, // 5 minutes
		MaxBanStrikes:        3,
	}
}

// sessionBucket tracks request counts for a single session.
type sessionBucket struct {
	mu               sync.Mutex
	requestsMinute   []time.Time
	requestsHour     []time.Time
	commandsMinute   []time.Time
	banUntil         time.Time
	banStrikes       int
	permanentlyBanned bool
}

// RateLimitVerdict is the result of a rate limit check.
type RateLimitVerdict struct {
	// Allowed indicates whether the request should be permitted.
	Allowed bool
	// Reason is a human-readable explanation.
	Reason string
	// RetryAfter is when the session can retry (zero if allowed).
	RetryAfter time.Time
	// PermanentlyBanned indicates the session is permanently blocked.
	PermanentlyBanned bool
}

// RateLimiter enforces per-session request and command rate limits.
// It uses a sliding window algorithm for accurate rate limiting.
type RateLimiter struct {
	mu       sync.RWMutex
	config   RateLimitConfig
	sessions map[string]*sessionBucket
	log      *AuditLog
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter(cfg RateLimitConfig, log *AuditLog) *RateLimiter {
	rl := &RateLimiter{
		config:   cfg,
		sessions: make(map[string]*sessionBucket),
		log:      log,
	}
	// Start cleanup goroutine
	go rl.cleanupLoop()
	return rl
}

// CheckRequest validates whether a session is allowed to make a request.
func (r *RateLimiter) CheckRequest(sessionID string) RateLimitVerdict {
	bucket := r.getOrCreateBucket(sessionID)
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()

	// Check permanent ban
	if bucket.permanentlyBanned {
		return RateLimitVerdict{
			Allowed:           false,
			Reason:            "Session is permanently banned due to repeated violations",
			PermanentlyBanned: true,
		}
	}

	// Check temporary ban
	if now.Before(bucket.banUntil) {
		return RateLimitVerdict{
			Allowed:     false,
			Reason:      fmt.Sprintf("Session is temporarily banned until %s", bucket.banUntil.Format(time.RFC3339)),
			RetryAfter:  bucket.banUntil,
		}
	}

	// Slide the windows
	bucket.requestsMinute = slideWindow(bucket.requestsMinute, now, time.Minute)
	bucket.requestsHour = slideWindow(bucket.requestsHour, now, time.Hour)

	// Check per-minute limit
	if len(bucket.requestsMinute) >= r.config.MaxRequestsPerMinute {
		r.applyBan(bucket, sessionID, "per-minute request limit exceeded")
		return RateLimitVerdict{
			Allowed:    false,
			Reason:     fmt.Sprintf("Rate limit exceeded: %d requests per minute", r.config.MaxRequestsPerMinute),
			RetryAfter: bucket.banUntil,
		}
	}

	// Check per-hour limit
	if len(bucket.requestsHour) >= r.config.MaxRequestsPerHour {
		r.applyBan(bucket, sessionID, "per-hour request limit exceeded")
		return RateLimitVerdict{
			Allowed:    false,
			Reason:     fmt.Sprintf("Rate limit exceeded: %d requests per hour", r.config.MaxRequestsPerHour),
			RetryAfter: bucket.banUntil,
		}
	}

	// Record this request
	bucket.requestsMinute = append(bucket.requestsMinute, now)
	bucket.requestsHour = append(bucket.requestsHour, now)

	return RateLimitVerdict{Allowed: true, Reason: "Request allowed"}
}

// CheckCommand validates whether a session is allowed to execute a shell command.
func (r *RateLimiter) CheckCommand(sessionID string) RateLimitVerdict {
	bucket := r.getOrCreateBucket(sessionID)
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()

	// Check permanent ban
	if bucket.permanentlyBanned {
		return RateLimitVerdict{
			Allowed:           false,
			Reason:            "Session is permanently banned",
			PermanentlyBanned: true,
		}
	}

	// Slide the window
	bucket.commandsMinute = slideWindow(bucket.commandsMinute, now, time.Minute)

	// Check per-minute command limit
	if len(bucket.commandsMinute) >= r.config.MaxCommandsPerMinute {
		r.applyBan(bucket, sessionID, "per-minute command limit exceeded")
		return RateLimitVerdict{
			Allowed:    false,
			Reason:     fmt.Sprintf("Command rate limit exceeded: %d commands per minute", r.config.MaxCommandsPerMinute),
			RetryAfter: bucket.banUntil,
		}
	}

	// Record this command
	bucket.commandsMinute = append(bucket.commandsMinute, now)

	return RateLimitVerdict{Allowed: true, Reason: "Command allowed"}
}

// GetStats returns rate limiting statistics for a session.
func (r *RateLimiter) GetStats(sessionID string) map[string]interface{} {
	bucket := r.getOrCreateBucket(sessionID)
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()
	bucket.requestsMinute = slideWindow(bucket.requestsMinute, now, time.Minute)
	bucket.requestsHour = slideWindow(bucket.requestsHour, now, time.Hour)
	bucket.commandsMinute = slideWindow(bucket.commandsMinute, now, time.Minute)

	return map[string]interface{}{
		"requests_this_minute": len(bucket.requestsMinute),
		"requests_this_hour":   len(bucket.requestsHour),
		"commands_this_minute": len(bucket.commandsMinute),
		"ban_strikes":          bucket.banStrikes,
		"permanently_banned":   bucket.permanentlyBanned,
		"banned_until":         bucket.banUntil,
	}
}

// ============================================================
// Private Methods
// ============================================================

func (r *RateLimiter) getOrCreateBucket(sessionID string) *sessionBucket {
	r.mu.Lock()
	defer r.mu.Unlock()

	if bucket, ok := r.sessions[sessionID]; ok {
		return bucket
	}

	bucket := &sessionBucket{}
	r.sessions[sessionID] = bucket
	return bucket
}

func (r *RateLimiter) applyBan(bucket *sessionBucket, sessionID, reason string) {
	bucket.banStrikes++
	if bucket.banStrikes >= r.config.MaxBanStrikes {
		bucket.permanentlyBanned = true
		if r.log != nil {
			r.log.Record(AuditEntry{
				Timestamp:   time.Now(),
				EventType:   EventRateLimitExceeded,
				Severity:    SeverityCritical,
				SessionID:   sessionID,
				Details:     fmt.Sprintf("Session PERMANENTLY BANNED after %d strikes: %s", bucket.banStrikes, reason),
				ThreatScore: 100,
			})
		}
	} else {
		bucket.banUntil = time.Now().Add(time.Duration(r.config.BanDurationSeconds) * time.Second)
		if r.log != nil {
			r.log.Record(AuditEntry{
				Timestamp:   time.Now(),
				EventType:   EventRateLimitExceeded,
				Severity:    SeverityHigh,
				SessionID:   sessionID,
				Details:     fmt.Sprintf("Session temporarily banned (strike %d/%d): %s", bucket.banStrikes, r.config.MaxBanStrikes, reason),
				ThreatScore: 70,
			})
		}
	}
}

// slideWindow removes timestamps older than the window duration.
func slideWindow(timestamps []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	i := 0
	for i < len(timestamps) && timestamps[i].Before(cutoff) {
		i++
	}
	return timestamps[i:]
}

// cleanupLoop periodically removes stale sessions from memory.
func (r *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for id, bucket := range r.sessions {
			bucket.mu.Lock()
			// Remove sessions that have been inactive for over 1 hour
			// and are not permanently banned
			if !bucket.permanentlyBanned && now.After(bucket.banUntil) &&
				len(bucket.requestsHour) == 0 {
				delete(r.sessions, id)
			}
			bucket.mu.Unlock()
		}
		r.mu.Unlock()
	}
}
