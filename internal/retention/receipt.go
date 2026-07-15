package retention

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	stableIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type closureReceipt struct {
	SchemaVersion    string `json:"schema_version"`
	ClosureReceiptID string `json:"closure_receipt_id"`
	IterationRunID   string `json:"iteration_run_id"`
	ProductLine      string `json:"product_line"`
	Service          string `json:"service"`
	Fingerprint      string `json:"fingerprint"`
	CaseSnapshot     struct {
		SnapshotID     string    `json:"snapshot_id"`
		ThroughCaseID  int64     `json:"through_case_id"`
		CaseIDs        []int64   `json:"case_ids"`
		ManifestSHA256 string    `json:"manifest_sha256"`
		VerifiedAt     time.Time `json:"verified_at"`
	} `json:"case_snapshot"`
	Rule struct {
		CandidateID string    `json:"candidate_id"`
		RuleID      string    `json:"rule_id"`
		PackVersion string    `json:"pack_version"`
		PackSHA256  string    `json:"pack_sha256"`
		ApprovedAt  time.Time `json:"approved_at"`
	} `json:"rule"`
	Evaluation struct {
		EvalRunID      string `json:"eval_run_id"`
		Status         string `json:"status"`
		ArtifactSHA256 string `json:"artifact_sha256"`
	} `json:"evaluation"`
	Deployment struct {
		Status       string    `json:"status"`
		BuildVersion string    `json:"build_version,omitempty"`
		BuildCommit  string    `json:"build_commit,omitempty"`
		DeployedAt   time.Time `json:"deployed_at,omitempty"`
	} `json:"deployment"`
	Observation struct {
		Status         string    `json:"status"`
		EndsAt         time.Time `json:"ends_at"`
		ValidatedAt    time.Time `json:"validated_at,omitempty"`
		EvidenceSHA256 string    `json:"evidence_sha256,omitempty"`
	} `json:"observation"`
	Status            string    `json:"status"`
	RetentionEligible bool      `json:"retention_eligible"`
	CreatedAt         time.Time `json:"created_at"`
}

type loadedReceipt struct {
	path    string
	digest  string
	receipt closureReceipt
	reasons []string
}

type indexedArtifact struct {
	display  string
	absolute string
	bytes    int64
}

type artifactIndex map[string][]indexedArtifact

func buildArtifactIndex(roots ...string) (artifactIndex, []Artifact, error) {
	index := artifactIndex{}
	var inputs []Artifact
	seenRoots := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, err
		}
		if seenRoots[absolute] {
			continue
		}
		seenRoots[absolute] = true
		info, err := os.Stat(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}
		if !info.IsDir() {
			return nil, nil, fmt.Errorf("artifact root is not a directory: %s", root)
		}
		err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			sum, size, err := hashFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(absolute, path)
			if err != nil {
				return err
			}
			identity := filepath.ToSlash(filepath.Join(filepath.Base(absolute), relative))
			index[sum] = append(index[sum], indexedArtifact{display: identity, absolute: path, bytes: size})
			inputs = append(inputs, Artifact{Path: identity, SHA256: sum, Bytes: size})
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	for digest := range index {
		sort.Slice(index[digest], func(i, j int) bool { return index[digest][i].display < index[digest][j].display })
	}
	return index, inputs, nil
}

func loadReceipts(root string, artifacts artifactIndex) ([]loadedReceipt, []ReceiptDiagnostic, error) {
	if root == "" {
		return nil, nil, nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(absolute); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var receipts []loadedReceipt
	var diagnostics []ReceiptDiagnostic
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var envelope struct {
			SchemaVersion string `json:"schema_version"`
			ProductLine   string `json:"product_line"`
			Service       string `json:"service"`
			Fingerprint   string `json:"fingerprint"`
		}
		if json.Unmarshal(data, &envelope) != nil || envelope.SchemaVersion != "cora.closure-receipt.v1" {
			return nil
		}
		relative, _ := filepath.Rel(absolute, path)
		sum := sha256.Sum256(data)
		loaded := loadedReceipt{path: filepath.ToSlash(relative), digest: hex.EncodeToString(sum[:])}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&loaded.receipt); err != nil {
			diagnostics = append(diagnostics, ReceiptDiagnostic{Path: loaded.path, ProductLine: envelope.ProductLine, Service: envelope.Service, Fingerprint: envelope.Fingerprint, Reasons: []string{"receipt_schema_invalid"}})
			return nil
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			diagnostics = append(diagnostics, ReceiptDiagnostic{Path: loaded.path, ProductLine: envelope.ProductLine, Service: envelope.Service, Fingerprint: envelope.Fingerprint, Reasons: []string{"receipt_schema_invalid"}})
			return nil
		}
		loaded.reasons = validateReceipt(loaded.receipt, artifacts)
		receipts = append(receipts, loaded)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(receipts, func(i, j int) bool {
		a, b := receipts[i].receipt, receipts[j].receipt
		if a.ProductLine != b.ProductLine {
			return a.ProductLine < b.ProductLine
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Fingerprint != b.Fingerprint {
			return a.Fingerprint < b.Fingerprint
		}
		return receipts[i].path < receipts[j].path
	})
	return receipts, diagnostics, nil
}

func validateReceipt(r closureReceipt, artifacts artifactIndex) []string {
	var reasons []string
	add := func(ok bool, reason string) {
		if !ok {
			reasons = append(reasons, reason)
		}
	}
	add(r.SchemaVersion == "cora.closure-receipt.v1", "receipt_schema_invalid")
	add(stableIDPattern.MatchString(r.ClosureReceiptID) && stableIDPattern.MatchString(r.IterationRunID), "receipt_identity_invalid")
	add(r.ProductLine != "" && r.Service != "" && fingerprintPattern.MatchString(r.Fingerprint), "receipt_problem_identity_invalid")
	add(r.CaseSnapshot.SnapshotID != "" && r.CaseSnapshot.ThroughCaseID > 0 && len(r.CaseSnapshot.CaseIDs) > 0 && !r.CaseSnapshot.VerifiedAt.IsZero(), "case_snapshot_invalid")
	add(validUniquePositiveIDs(r.CaseSnapshot.CaseIDs, r.CaseSnapshot.ThroughCaseID), "case_snapshot_case_ids_invalid")
	add(hasArtifact(artifacts, r.CaseSnapshot.ManifestSHA256), "case_manifest_hash_missing")
	manifestReasons := validateCaseManifest(r, artifacts)
	reasons = append(reasons, manifestReasons...)
	add(stableIDPattern.MatchString(r.Rule.CandidateID) && r.Rule.RuleID != "" && r.Rule.PackVersion != "" && !r.Rule.ApprovedAt.IsZero(), "rule_not_reviewed")
	add(hasArtifact(artifacts, r.Rule.PackSHA256), "rule_pack_hash_missing")
	add(stableIDPattern.MatchString(r.Evaluation.EvalRunID) && r.Evaluation.Status == "passed", "evaluation_not_passed")
	add(hasArtifact(artifacts, r.Evaluation.ArtifactSHA256), "evaluation_hash_missing")
	add(r.Deployment.Status == "deployed" && r.Deployment.BuildVersion != "" && regexp.MustCompile(`^[0-9a-f]{7,64}$`).MatchString(r.Deployment.BuildCommit) && !r.Deployment.DeployedAt.IsZero(), "deployment_not_verified")
	add(r.Observation.Status == "passed" && !r.Observation.EndsAt.IsZero() && !r.Observation.ValidatedAt.IsZero(), "observation_not_passed")
	add(!r.Observation.ValidatedAt.Before(r.Observation.EndsAt), "observation_window_incomplete")
	add(hasArtifact(artifacts, r.Observation.EvidenceSHA256), "observation_hash_missing")
	add(r.Status == "validated", "receipt_not_validated")
	add(r.RetentionEligible, "receipt_not_retention_eligible")
	add(!r.CreatedAt.IsZero(), "receipt_created_at_invalid")
	return sortedUnique(reasons)
}

func hasArtifact(index artifactIndex, digest string) bool {
	return sha256Pattern.MatchString(digest) && len(index[digest]) > 0
}

func validateCaseManifest(receipt closureReceipt, artifacts artifactIndex) []string {
	matches := artifacts[receipt.CaseSnapshot.ManifestSHA256]
	if len(matches) == 0 {
		return nil
	}
	data, err := os.ReadFile(matches[0].absolute)
	if err != nil {
		return []string{"case_manifest_invalid"}
	}
	var manifest struct {
		SchemaVersion      string `json:"schema_version"`
		SnapshotID         string `json:"snapshot_id"`
		ProductLine        string `json:"product_line"`
		ThroughCaseID      int64  `json:"through_case_id"`
		CaseCount          int    `json:"case_count"`
		CaseSnapshotSHA256 string `json:"case_snapshot_sha256"`
		Pages              []any  `json:"pages"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return []string{"case_manifest_invalid"}
	}
	var reasons []string
	if manifest.SchemaVersion != "cora.case-snapshot-manifest.v1" || manifest.SnapshotID != receipt.CaseSnapshot.SnapshotID || manifest.ProductLine != receipt.ProductLine || manifest.ThroughCaseID != receipt.CaseSnapshot.ThroughCaseID || manifest.CaseCount < len(receipt.CaseSnapshot.CaseIDs) || len(manifest.Pages) == 0 {
		reasons = append(reasons, "case_manifest_identity_mismatch")
	}
	if !hasArtifact(artifacts, manifest.CaseSnapshotSHA256) {
		reasons = append(reasons, "case_snapshot_hash_missing")
	}
	return reasons
}

func referencedCaseSnapshotDigest(receipt closureReceipt, artifacts artifactIndex) string {
	matches := artifacts[receipt.CaseSnapshot.ManifestSHA256]
	if len(matches) == 0 {
		return ""
	}
	data, err := os.ReadFile(matches[0].absolute)
	if err != nil {
		return ""
	}
	var manifest struct {
		CaseSnapshotSHA256 string `json:"case_snapshot_sha256"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return manifest.CaseSnapshotSHA256
}

func validUniquePositiveIDs(ids []int64, through int64) bool {
	seen := map[int64]bool{}
	for _, id := range ids {
		if id < 1 || id > through || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func hashFile(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func sortedUnique(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
