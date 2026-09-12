package specflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var specIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

const featuresDirectoryPath = "docs/changes/features"

func displayFeaturesDirectory() string { return featuresDirectoryPath }

// PrepareFeaturesDirectory returns the common write root for the first agent
// turn. It is created up front because some agent runtimes require a writable
// root to already exist before a turn begins.
func PrepareFeaturesDirectory(root string) (string, error) {
	root, err := canonicalExisting(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git root: %w", err)
	}
	directory := filepath.Join(root, filepath.FromSlash(featuresDirectoryPath))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("create feature directory %q: %w", directory, err)
	}
	if err := CheckContainment(root, directory); err != nil {
		return "", err
	}
	return directory, nil
}

func featureEntries(directory string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		result[entry.Name()] = struct{}{}
	}
	return result, nil
}

func FindGitRoot(ctx context.Context, start string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", start, "rev-parse", "--show-toplevel").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("find Git root from %q: %w: %s", start, err, strings.TrimSpace(string(output)))
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("find Git root from %q: git returned an empty path", start)
	}
	return canonicalExisting(root)
}

func ValidateFeatureID(featureID string) error {
	if featureID == "" {
		return fmt.Errorf("feature ID must not be empty")
	}
	if len(featureID) > 64 {
		return fmt.Errorf("feature ID must not exceed 64 characters")
	}
	upper := strings.ToUpper(featureID)
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
		len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		return fmt.Errorf("feature ID %q is a reserved Windows device name", featureID)
	}
	if !specIDPattern.MatchString(featureID) {
		return fmt.Errorf("feature ID must start and end with [a-z0-9] and otherwise match [a-z0-9-]+")
	}
	return nil
}

// FeatureTarget names every durable artifact in one planning flow.
type FeatureTarget struct {
	ID               string
	Directory        string
	IntentPath       string
	SpecPath         string
	PlanPath         string
	JournalPath      string
	StatePath        string
	ReviewsDirectory string
}

func (t FeatureTarget) DocumentPath(stage Stage) (string, error) {
	switch stage {
	case StageIntent:
		return t.IntentPath, nil
	case StageSpec:
		return t.SpecPath, nil
	case StagePlan:
		return t.PlanPath, nil
	default:
		return "", domainError("stage", stage)
	}
}

func (t FeatureTarget) DisplayDocumentPath(stage Stage) (string, error) {
	if !stage.Valid() {
		return "", domainError("stage", stage)
	}
	return path.Join(featuresDirectoryPath, t.ID, string(stage)+".md"), nil
}

func (t FeatureTarget) ArtifactFilename(stage Stage, review bool) (string, error) {
	if review {
		if stage != StageSpec && stage != StagePlan {
			return "", fmt.Errorf("%w: %s has no review artifact", ErrInvalidDomainValue, stage)
		}
		return "review.md", nil
	}
	if !stage.Valid() {
		return "", domainError("stage", stage)
	}
	return string(stage) + ".md", nil
}

func PrepareFeatureTarget(root, date, featureID string) (FeatureTarget, error) {
	if err := ValidateFeatureID(featureID); err != nil {
		return FeatureTarget{}, err
	}
	root, err := canonicalExisting(root)
	if err != nil {
		return FeatureTarget{}, err
	}
	base := filepath.Join(root, filepath.FromSlash(featuresDirectoryPath))
	if err := os.MkdirAll(base, 0o755); err != nil {
		return FeatureTarget{}, err
	}
	for suffix := 1; ; suffix++ {
		id := date + "-" + featureID
		if suffix > 1 {
			id += fmt.Sprintf("-%d", suffix)
		}
		directory := filepath.Join(base, id)
		if _, err := os.Lstat(directory); os.IsNotExist(err) {
			if err := CheckContainment(root, directory); err != nil {
				return FeatureTarget{}, err
			}
			return featureTarget(root, id)
		} else if err != nil {
			return FeatureTarget{}, err
		}
	}
}

func FeatureTargetForID(root, featureID string) (FeatureTarget, error) {
	if err := ValidateFeatureID(featureID); err != nil {
		return FeatureTarget{}, err
	}
	root, err := canonicalExisting(root)
	if err != nil {
		return FeatureTarget{}, err
	}
	return featureTarget(root, featureID)
}

func featureTarget(root, id string) (FeatureTarget, error) {
	directory := filepath.Join(root, filepath.FromSlash(featuresDirectoryPath), id)
	if err := CheckContainment(root, directory); err != nil {
		return FeatureTarget{}, err
	}
	return FeatureTarget{
		ID:               id,
		Directory:        directory,
		IntentPath:       filepath.Join(directory, "intent.md"),
		SpecPath:         filepath.Join(directory, "spec.md"),
		PlanPath:         filepath.Join(directory, "plan.md"),
		JournalPath:      filepath.Join(directory, "mem-log.md"),
		StatePath:        filepath.Join(directory, "state.json"),
		ReviewsDirectory: filepath.Join(directory, "reviews"),
	}, nil
}

func CreateArtifactRoot(workspace string) (string, error) {
	workspace, err := canonicalExisting(workspace)
	if err != nil {
		return "", err
	}
	root, err := os.MkdirTemp("", "stepan-artifact-")
	if err != nil {
		return "", err
	}
	canonical, err := canonicalExisting(root)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	if relative, err := filepath.Rel(workspace, canonical); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("artifact root must be outside Git workspace")
	}
	return canonical, nil
}

func CheckContainment(root, target string) error {
	root, err := canonicalExisting(root)
	if err != nil {
		return fmt.Errorf("canonicalize Git root: %w", err)
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("target path %q must be absolute", target)
	}
	parent, err := canonicalNearest(target)
	if err != nil {
		return fmt.Errorf("canonicalize target %q: %w", target, err)
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target %q escapes Git root %q through %q", target, root, parent)
	}
	return nil
}

func canonicalExisting(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return resolveReparsePoints(absolute)
}

func canonicalNearest(value string) (string, error) {
	probe := filepath.Clean(value)
	for {
		if _, err := os.Lstat(probe); err == nil {
			return resolveReparsePoints(probe)
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("no existing parent")
		}
		probe = parent
	}
}

func resolveReparsePoints(value string) (string, error) {
	value = filepath.Clean(value)
	for links := 0; links < 255; links++ {
		volume := filepath.VolumeName(value)
		current := volume + string(filepath.Separator)
		components := strings.Split(strings.TrimPrefix(value, current), string(filepath.Separator))
		resolved := false
		for index, component := range components {
			if component == "" {
				continue
			}
			candidate := filepath.Join(current, component)
			info, err := os.Lstat(candidate)
			if err != nil {
				return "", err
			}
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
				current = candidate
				continue
			}
			destination, err := os.Readlink(candidate)
			if err != nil {
				return "", fmt.Errorf("unsupported reparse point %q: %w", candidate, err)
			}
			if !filepath.IsAbs(destination) {
				destination = filepath.Join(filepath.Dir(candidate), destination)
			}
			value = filepath.Join(append([]string{destination}, components[index+1:]...)...)
			resolved = true
			break
		}
		if !resolved {
			return filepath.EvalSymlinks(value)
		}
	}
	return "", fmt.Errorf("too many reparse points in %q", value)
}
