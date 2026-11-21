package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

const (
	VerificationManual      = "manual"
	VerificationExactText   = "exact_text"
	VerificationURLContains = "url_contains"
	VerificationURLDomain   = "url_domain"

	SubmissionPendingReview = "pending_review"
	SubmissionFlaggedReview = "flagged_review"
)

type VerificationDecision struct {
	Status         string
	ProofHash      string
	PrecheckStatus string
	Flags          []string
}

// evaluateSubmission is intentionally deterministic. It never claims that a screenshot
// proves an action, and it never fetches arbitrary user URLs. Unknown verification modes
// always fall back to human review.
func (s *TaskFlowStore) evaluateSubmission(ctx context.Context, workerID int64, task Task, proof string) VerificationDecision {
	normalized := strings.ToLower(strings.TrimSpace(proof))
	hash := sha256.Sum256([]byte(normalized))
	decision := VerificationDecision{
		Status:         SubmissionPendingReview,
		ProofHash:      hex.EncodeToString(hash[:]),
		PrecheckStatus: "not_configured",
	}

	addFlag := func(flag string) {
		for _, existing := range decision.Flags {
			if existing == flag {
				return
			}
		}
		decision.Flags = append(decision.Flags, flag)
	}

	if strings.HasPrefix(strings.ToLower(proof), "photo_file_id:") {
		addFlag("photo_requires_human_review")
	}
	lowerProof := strings.ToLower(proof)
	for _, sensitive := range []string{"private key", "seed phrase", "recovery phrase", "mnemonic phrase"} {
		if strings.Contains(lowerProof, sensitive) {
			addFlag("possible_sensitive_secret")
		}
	}

	// Strict duplicate-evidence control across all tasks and accounts.
	duplicateCount, err := s.submissions.CountDocuments(ctx, bson.M{"proof_hash": decision.ProofHash})
	if err != nil || duplicateCount > 0 {
		addFlag("duplicate_proof")
	}

	// Excessive submission velocity is held for review rather than auto-approved.
	cutoff := time.Now().UTC().Add(-10 * time.Minute)
	recentCount, err := s.submissions.CountDocuments(ctx, bson.M{
		"worker_id":  workerID,
		"created_at": bson.M{"$gte": cutoff},
	})
	if err != nil || recentCount >= 4 {
		addFlag("high_submission_velocity")
	}

	var user User
	if err := s.users.FindOne(ctx, bson.M{"telegram_id": workerID}).Decode(&user); err != nil {
		addFlag("worker_account_unverified")
	} else if user.Status != "active" {
		addFlag("worker_account_not_active")
	}

	if strings.Contains(lowerProof, "http://") || strings.Contains(lowerProof, "https://") {
		parsed, parseErr := extractProofURL(proof)
		if parseErr != nil {
			addFlag("invalid_or_unsafe_url")
		} else if parsed.Scheme != "https" {
			addFlag("url_not_https")
		} else if isPrivateOrLocalHost(parsed.Hostname()) {
			addFlag("private_or_local_url")
		}
	}

	// Any strict-risk signal blocks automatic approval.
	if len(decision.Flags) > 0 {
		decision.Status = SubmissionFlaggedReview
		return decision
	}

	mode := strings.ToLower(strings.TrimSpace(task.VerificationMode))
	expected := strings.TrimSpace(task.VerificationValue)
	if mode == "" || mode == VerificationManual {
		decision.PrecheckStatus = "manual_review_required"
		return decision
	}
	if mode == "trusted_api" {
		addFlag("trusted_api_provider_not_configured")
		decision.Status = SubmissionFlaggedReview
		decision.PrecheckStatus = "provider_unavailable"
		return decision
	}
	if expected == "" {
		addFlag("verification_rule_missing_value")
		decision.Status = SubmissionFlaggedReview
		return decision
	}

	verified := false
	switch mode {
	case VerificationExactText:
		// Exact text is suitable only for tasks whose completion token is generated
		// by a trusted external system. It is not suitable for subjective work.
		verified = strings.EqualFold(strings.TrimSpace(proof), expected)
	case VerificationURLContains:
		parsed, parseErr := extractProofURL(proof)
		verified = parseErr == nil && parsed.Scheme == "https" &&
			!isPrivateOrLocalHost(parsed.Hostname()) &&
			strings.Contains(strings.ToLower(parsed.String()), strings.ToLower(expected))
	case VerificationURLDomain:
		parsed, parseErr := extractProofURL(proof)
		expectedHost := strings.ToLower(strings.TrimPrefix(expected, "www."))
		actualHost := ""
		if parseErr == nil {
			actualHost = strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
		}
		verified = parseErr == nil && parsed.Scheme == "https" &&
			!isPrivateOrLocalHost(parsed.Hostname()) && actualHost == expectedHost
	default:
		// Unknown rule: fail closed into human review.
		addFlag("unknown_verification_mode")
		decision.Status = SubmissionFlaggedReview
		return decision
	}

	if !verified {
		addFlag("verification_rule_not_matched")
		decision.Status = SubmissionFlaggedReview
		decision.PrecheckStatus = "format_check_failed"
		return decision
	}
	// A matching URL/text format is not proof that the external action happened.
	// Keep the submission in the Seller review queue until a trusted provider API
	// can attest completion. This prevents false automatic payouts.
	decision.Status = SubmissionPendingReview
	decision.PrecheckStatus = "format_passed_action_unverified"
	return decision
}

func extractProofURL(proof string) (*url.URL, error) {
	for _, token := range strings.Fields(proof) {
		token = strings.Trim(token, "<>\"'()[]{}.,;!")
		if !strings.HasPrefix(strings.ToLower(token), "https://") && !strings.HasPrefix(strings.ToLower(token), "http://") {
			continue
		}
		parsed, err := url.Parse(token)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil {
			return nil, fmt.Errorf("invalid URL")
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("no URL found")
}

func isPrivateOrLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

// SetTaskVerification configures a deterministic rule. ownerID=0 is reserved for an
// Admin caller that has already passed the Admin allowlist check.
func (s *TaskFlowStore) SetTaskVerification(ctx context.Context, taskID primitive.ObjectID, ownerID int64, mode, value string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	value = strings.TrimSpace(value)
	if mode != VerificationManual && mode != VerificationExactText && mode != VerificationURLContains && mode != VerificationURLDomain {
		return fmt.Errorf("mode must be manual, exact_text, url_contains, or url_domain")
	}
	if mode == VerificationManual {
		value = ""
	} else if len([]rune(value)) < 3 || len([]rune(value)) > 240 {
		return fmt.Errorf("verification value must be 3–240 characters")
	}
	if mode == VerificationURLDomain {
		value = strings.ToLower(strings.TrimPrefix(value, "https://"))
		value = strings.TrimPrefix(value, "www.")
		value = strings.TrimSuffix(value, "/")
		parsed, parseErr := url.Parse("https://" + value)
		if parseErr != nil || parsed.Hostname() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.User != nil || parsed.Host != value {
			return fmt.Errorf("url_domain value must be a hostname only, such as example.com")
		}
	}
	filter := bson.M{"_id": taskID, "status": bson.M{"$nin": []string{TaskStatusDeleted, TaskStatusRejected}}}
	if ownerID > 0 {
		filter["creator_id"] = ownerID
	}
	result, err := s.tasks.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"verification_mode":  mode,
		"verification_value": value,
		"updated_at":         time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}

// CountReviewQueue is used by Admin/Seller dashboards to show only exceptional work.
func (s *TaskFlowStore) CountReviewQueue(ctx context.Context, sellerID int64) (int64, error) {
	filter := bson.M{"status": bson.M{"$in": []string{SubmissionPendingReview, SubmissionFlaggedReview}}}
	if sellerID > 0 {
		filter["seller_id"] = sellerID
	}
	return s.submissions.CountDocuments(ctx, filter)
}

func (a *App) handleSetTaskVerification(ctx context.Context, msg *TelegramMessage, fields []string) {
	if len(fields) < 4 {
		a.sendPlain(ctx, msg.Chat.ID, "Usage: /set_task_verification TASK_ID MODE VALUE\nModes: manual | exact_text | url_contains | url_domain\nThese rules validate proof format only; they do not auto-approve work. For manual mode, use: /set_task_verification TASK_ID manual none")
		return
	}
	id, err := primitive.ObjectIDFromHex(fields[1])
	if err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Invalid Task ID.")
		return
	}
	ownerID := msg.From.ID
	if a.IsAdmin(msg.From.ID) {
		ownerID = 0
	} else {
		user, userErr := a.Users.GetByTelegramID(ctx, msg.From.ID)
		if userErr != nil || user.Role != "seller" {
			a.sendPlain(ctx, msg.Chat.ID, "Only the task owner with verified Seller access or an Admin can configure verification.")
			return
		}
		paid, payErr := a.Payments.HasPaidSellerRegistration(ctx, msg.From.ID)
		if payErr != nil || !paid {
			a.sendPlain(ctx, msg.Chat.ID, "Verified Seller registration is required.")
			return
		}
	}
	mode := strings.ToLower(strings.TrimSpace(fields[2]))
	value := strings.TrimSpace(strings.Join(fields[3:], " "))
	if mode == VerificationManual && strings.EqualFold(value, "none") {
		value = ""
	}
	if err := a.TaskFlow.SetTaskVerification(ctx, id, ownerID, mode, value); err != nil {
		a.sendPlain(ctx, msg.Chat.ID, "Verification rule was not saved: "+err.Error())
		return
	}
	a.sendPlain(ctx, msg.Chat.ID, fmt.Sprintf("✅ Task verification pre-check updated.\nMode: %s\nValue: %s\n\nStrict fraud checks remain enabled. Duplicate, risky, unsupported, or mismatched proof is held for human review. A matching text/URL format is not proof that the external task was completed, so Seller approval is still required until a trusted provider API is integrated.", mode, value))
}
