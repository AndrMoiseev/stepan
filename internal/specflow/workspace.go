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

var specIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)

type SpecTarget struct {
	Directory   string
	Entrypoint  string
	DisplayPath string
}

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

func ValidateSpecID(specID string) error {
	if specID == "" {
		return fmt.Errorf("spec-id must not be empty")
	}
	if len(specID) > 64 {
		return fmt.Errorf("spec-id must not exceed 64 characters")
	}
	upper := strings.ToUpper(specID)
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
		len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		return fmt.Errorf("spec-id %q is a reserved Windows device name", specID)
	}
	if !specIDPattern.MatchString(specID) {
		return fmt.Errorf("spec-id must match [a-z0-9-]+")
	}
	return nil
}

func PrepareSpecTarget(root, specID string) (SpecTarget, error) {
	target, err := SpecTargetForID(root, specID)
	if err != nil {
		return SpecTarget{}, err
	}
	directory := target.Directory
	if _, err := os.Lstat(directory); err == nil {
		return SpecTarget{}, fmt.Errorf("specification directory %q already exists", directory)
	} else if !os.IsNotExist(err) {
		return SpecTarget{}, fmt.Errorf("check specification directory %q: %w", directory, err)
	}
	return target, nil
}

func SpecTargetForID(root, specID string) (SpecTarget, error) {
	if err := ValidateSpecID(specID); err != nil {
		return SpecTarget{}, err
	}
	root, err := canonicalExisting(root)
	if err != nil {
		return SpecTarget{}, fmt.Errorf("canonicalize Git root: %w", err)
	}
	directory := filepath.Join(root, filepath.FromSlash(featuresDirectoryPath), specID)
	if err := CheckContainment(root, directory); err != nil {
		return SpecTarget{}, err
	}
	return SpecTarget{
		Directory:   directory,
		Entrypoint:  filepath.Join(directory, "specification.md"),
		DisplayPath: path.Join(featuresDirectoryPath, specID, "specification.md"),
	}, nil
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
