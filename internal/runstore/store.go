package runstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

var (
	artifactLocks          sync.Map // map[string]*sync.Mutex, keyed by final artifact path
	failedPublications     sync.Map // map[string]struct{}, finalization that needs cleanup
	finalizePublication    = publishFinalFile
	beforeArtifactOpenHook func(string)
)

const (
	// RunsDirectoryName is the directory below the Stepan data root that owns
	// every implementation run.
	RunsDirectoryName = "runs"
	// FilesDirectoryName is the run-local directory for immutable related
	// files. The journal and state database are added by later store layers.
	FilesDirectoryName = "files"
	markerSize         = sha256.Size*2 + 1
	verificationBuffer = 32 * 1024
)

var (
	// ErrInvalidRunID reports a run ID that cannot safely name a directory.
	ErrInvalidRunID = errors.New("invalid run ID")
	// ErrRunNotFound reports a run directory that has not been created.
	ErrRunNotFound = errors.New("implementation run not found")
	// ErrInvalidReference reports a malformed evidence reference.
	ErrInvalidReference = errors.New("invalid artifact reference")
	// ErrReferenceUnavailable reports a reference whose published file is not
	// available in this run.
	ErrReferenceUnavailable = errors.New("artifact reference is unavailable")
	// ErrReferenceIntegrity reports a published file that no longer matches the
	// digest recorded in its reference.
	ErrReferenceIntegrity = errors.New("artifact reference integrity failure")
	// ErrConflictingPublication reports an attempt to change an immutable file.
	ErrConflictingPublication = errors.New("conflicting artifact publication")
	// ErrUnsafePath reports a symlink or non-directory where the store needs a
	// directory it owns.
	ErrUnsafePath = errors.New("unsafe run store path")
)

// Store owns implementation runs below one Stepan data root. New accepts an
// explicit root so callers and tests do not need to use the user's home
// directory. Production callers normally use DefaultRoot(os.UserHomeDir()).
type Store struct {
	root string
	runs string
}

// RunIDs returns the identifiers of all durable run directories. It does not
// open projections or modify run data, so status inspection can use it while
// another process owns the controller lock.
func (s *Store) RunIDs() ([]implementationstate.RunID, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrUnsafePath)
	}
	if err := requireDirectory(s.root); err != nil {
		return nil, err
	}
	if err := requireDirectory(s.runs); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.runs)
	if err != nil {
		return nil, fmt.Errorf("list implementation runs: %w", err)
	}
	ids := make([]implementationstate.RunID, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect implementation run %q: %w", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("%w: %s", ErrUnsafePath, filepath.Join(s.runs, entry.Name()))
		}
		id := implementationstate.RunID(entry.Name())
		if _, err := validRunID(id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// DefaultRoot returns the Stepan data root for a supplied home directory.
func DefaultRoot(home string) string {
	return filepath.Join(home, ".stepan")
}

// New creates the Stepan data root and its runs directory when absent. It
// rejects a final root or runs directory that is a symlink, so artifact paths
// cannot escape through store-owned path components.
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty root", ErrUnsafePath)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve run store root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create run store root: %w", err)
	}
	if err := requireDirectory(absolute); err != nil {
		return nil, err
	}
	runs, err := childDirectory(absolute, RunsDirectoryName, true)
	if err != nil {
		return nil, err
	}
	return &Store{root: absolute, runs: runs}, nil
}

// Root reports the absolute Stepan data root supplied to New.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// RunsRoot reports the absolute directory containing run directories.
func (s *Store) RunsRoot() string {
	if s == nil {
		return ""
	}
	return s.runs
}

// Create creates (or reopens) one run directory and its related-files
// directory. It never deletes data, including for closed runs.
func (s *Store) Create(id implementationstate.RunID) (*Run, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrUnsafePath)
	}
	name, err := validRunID(id)
	if err != nil {
		return nil, err
	}
	if err := requireDirectory(s.root); err != nil {
		return nil, err
	}
	if err := requireDirectory(s.runs); err != nil {
		return nil, err
	}
	directory, err := childDirectory(s.runs, name, true)
	if err != nil {
		return nil, err
	}
	files, err := childDirectory(directory, FilesDirectoryName, true)
	if err != nil {
		return nil, err
	}
	return &Run{id: id, directory: directory, files: files}, nil
}

// Open returns an existing complete run layout without creating it.
func (s *Store) Open(id implementationstate.RunID) (*Run, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil store", ErrUnsafePath)
	}
	name, err := validRunID(id)
	if err != nil {
		return nil, err
	}
	if err := requireDirectory(s.root); err != nil {
		return nil, err
	}
	if err := requireDirectory(s.runs); err != nil {
		return nil, err
	}
	directory, err := childDirectory(s.runs, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	files, err := childDirectory(directory, FilesDirectoryName, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has no files directory", ErrRunNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return &Run{id: id, directory: directory, files: files}, nil
}

// Run is a single immutable-related-file namespace. Its directory is
// <root>/runs/<run-id>; Path and FilesPath make the fixed layout observable to
// the later journal and SQLite layers without accepting caller-provided paths.
type Run struct {
	id        implementationstate.RunID
	directory string
	files     string
}

func (r *Run) ID() implementationstate.RunID { return r.id }

func (r *Run) Path() string {
	if r == nil {
		return ""
	}
	return r.directory
}

func (r *Run) FilesPath() string {
	if r == nil {
		return ""
	}
	return r.files
}

// Publish stores data durably before returning a reference to it. The evidence
// ID remains immutable: publishing a different payload under it is rejected.
func (r *Run) Publish(id implementationstate.EvidenceID, data []byte) (implementationstate.EvidenceRef, error) {
	return r.PublishReader(id, bytes.NewReader(data))
}

// PublishReader streams a potentially large result into the run-local files
// directory. The final filename is derived from the evidence ID rather than a
// caller path; the returned SHA-256 reference can therefore be safely written
// into a later event only after this method succeeds.
func (r *Run) PublishReader(id implementationstate.EvidenceID, source io.Reader) (implementationstate.EvidenceRef, error) {
	if r == nil || source == nil {
		return implementationstate.EvidenceRef{}, fmt.Errorf("%w: nil run or reader", ErrInvalidReference)
	}
	if strings.TrimSpace(string(id)) == "" {
		return implementationstate.EvidenceRef{}, fmt.Errorf("%w: empty evidence ID", ErrInvalidReference)
	}
	if err := requireDirectory(r.directory); err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	if err := requireDirectory(r.files); err != nil {
		return implementationstate.EvidenceRef{}, err
	}

	temporary, err := os.CreateTemp(r.files, ".publish-*")
	if err != nil {
		return implementationstate.EvidenceRef{}, fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), source); err != nil {
		temporary.Close()
		return implementationstate.EvidenceRef{}, fmt.Errorf("write artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return implementationstate.EvidenceRef{}, fmt.Errorf("sync artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return implementationstate.EvidenceRef{}, fmt.Errorf("close artifact: %w", err)
	}

	reference := implementationstate.EvidenceRef{ID: id, Digest: hex.EncodeToString(hash.Sum(nil))}
	target := r.filePath(id)
	if err := r.publishTemporary(temporaryName, target, reference.Digest); err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return reference, nil
}

// VerifyReference checks that a previously published reference is present,
// regular, and unchanged. Event writers use it before accepting a reference.
func (r *Run) VerifyReference(reference implementationstate.EvidenceRef) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrReferenceUnavailable)
	}
	if err := validReference(reference); err != nil {
		return err
	}
	if err := requireDirectory(r.directory); err != nil {
		return err
	}
	if err := requireDirectory(r.files); err != nil {
		return err
	}
	target := r.filePath(reference.ID)
	if err := verifyMarker(r.markerPath(target), reference.Digest); err != nil {
		return err
	}
	return verifyFile(target, reference.Digest)
}

// Read returns a verified copy of a related file. It is deliberately routed
// through VerifyReference so recovery never silently consumes altered output.
func (r *Run) Read(reference implementationstate.EvidenceRef) ([]byte, error) {
	return r.readVerified(reference)
}

// ArtifactPath returns the absolute path of a published immutable artifact.
// The complete reference is verified before exposing the path, so callers can
// present only durable run-local files to agents or users. Consumers that need
// bytes must still use Read, which verifies the file digest through one handle.
func (r *Run) ArtifactPath(reference implementationstate.EvidenceRef) (string, error) {
	if err := r.VerifyReference(reference); err != nil {
		return "", err
	}
	return r.filePath(reference.ID), nil
}

func (r *Run) publishTemporary(temporary, target, digest string) error {
	lock := artifactLock(target)
	lock.Lock()
	defer lock.Unlock()

	marker := r.markerPath(target)
	if _, failed := failedPublications.Load(target); failed {
		if err := removeUnpublished(target, marker, r.files); err != nil {
			return fmt.Errorf("clear failed artifact publication: %w", err)
		}
		failedPublications.Delete(target)
	}
	if err := verifyPublication(target, marker, digest); err == nil {
		// A duplicate publisher still waits for a directory barrier. This makes
		// it impossible for a loser to return while the winning publication is
		// still awaiting its final durability step.
		if err := syncDirectory(r.files); err != nil {
			return fmt.Errorf("sync published artifact directory: %w", err)
		}
		return nil
	} else if !errors.Is(err, ErrReferenceUnavailable) {
		if errors.Is(err, ErrReferenceIntegrity) {
			return fmt.Errorf("%w: %s", ErrConflictingPublication, target)
		}
		return err
	}

	// A data file without a durable marker came from an interrupted or failed
	// publication and cannot be referenced. Remove it before retrying so a
	// later successful call always has its own final publication barrier.
	if err := removeUnpublished(target, marker, r.files); err != nil {
		return err
	}
	if err := finalizePublication(temporary, target); err != nil {
		return discardFailedPublication(target, marker, r.files, fmt.Errorf("publish artifact: %w", err))
	}
	if err := publishMarker(r.files, marker, digest); err != nil {
		return discardFailedPublication(target, marker, r.files, err)
	}
	return nil
}

func (r *Run) readVerified(reference implementationstate.EvidenceRef) ([]byte, error) {
	if err := r.verifyReferenceMarker(reference); err != nil {
		return nil, err
	}
	return readVerifiedFile(r.filePath(reference.ID), reference.Digest)
}

// verifyReferenceMarker validates a reference and its bounded publication
// marker. Read then verifies and materializes the artifact through one opened
// handle, while VerifyReference uses the streaming artifact verifier below.
func (r *Run) verifyReferenceMarker(reference implementationstate.EvidenceRef) error {
	if r == nil {
		return fmt.Errorf("%w: nil run", ErrReferenceUnavailable)
	}
	if err := validReference(reference); err != nil {
		return err
	}
	if err := requireDirectory(r.directory); err != nil {
		return err
	}
	if err := requireDirectory(r.files); err != nil {
		return err
	}
	target := r.filePath(reference.ID)
	if err := verifyMarker(r.markerPath(target), reference.Digest); err != nil {
		return err
	}
	return nil
}

func (r *Run) markerPath(target string) string { return target + ".published" }

func publishMarker(directory, marker, digest string) error {
	temporary, err := os.CreateTemp(directory, ".publish-marker-*")
	if err != nil {
		return fmt.Errorf("create publication marker: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := io.WriteString(temporary, digest+"\n"); err != nil {
		temporary.Close()
		return fmt.Errorf("write publication marker: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync publication marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close publication marker: %w", err)
	}
	if err := finalizePublication(temporaryName, marker); err != nil {
		return fmt.Errorf("publish artifact marker: %w", err)
	}
	return nil
}

func verifyPublication(target, marker, digest string) error {
	if err := verifyMarker(marker, digest); err != nil {
		return err
	}
	return verifyFile(target, digest)
}

func verifyMarker(path, digest string) error {
	return withRegularFile(path, func(file *os.File) error {
		data, err := io.ReadAll(io.LimitReader(file, markerSize+1))
		if err != nil {
			return fmt.Errorf("read publication marker: %w", err)
		}
		if len(data) != markerSize || string(data) != digest+"\n" {
			return fmt.Errorf("%w: publication marker does not match artifact", ErrReferenceIntegrity)
		}
		return nil
	})
}

func removeUnpublished(target, marker, directory string) error {
	removed := false
	for _, path := range []string{target, marker} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect unpublished artifact: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: unpublished artifact is not a regular file", ErrUnsafePath)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove unpublished artifact: %w", err)
		}
		removed = true
	}
	if removed {
		if err := syncDirectory(directory); err != nil {
			return fmt.Errorf("sync unpublished artifact removal: %w", err)
		}
	}
	return nil
}

func artifactLock(path string) *sync.Mutex {
	created := &sync.Mutex{}
	actual, _ := artifactLocks.LoadOrStore(path, created)
	return actual.(*sync.Mutex)
}

func discardFailedPublication(target, marker, directory string, publicationErr error) error {
	failedPublications.Store(target, struct{}{})
	if err := removeUnpublished(target, marker, directory); err != nil {
		return errors.Join(publicationErr, fmt.Errorf("clear failed publication: %w", err))
	}
	failedPublications.Delete(target)
	return publicationErr
}

func (r *Run) filePath(id implementationstate.EvidenceID) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(r.files, hex.EncodeToString(sum[:]))
}

func validRunID(id implementationstate.RunID) (string, error) {
	name := string(id)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("%w: %q", ErrInvalidRunID, name)
	}
	return name, nil
}

func validReference(reference implementationstate.EvidenceRef) error {
	if strings.TrimSpace(string(reference.ID)) == "" || len(reference.Digest) != sha256.Size*2 {
		return fmt.Errorf("%w", ErrInvalidReference)
	}
	decoded, err := hex.DecodeString(reference.Digest)
	if err != nil || len(decoded) != sha256.Size || reference.Digest != strings.ToLower(reference.Digest) {
		return fmt.Errorf("%w", ErrInvalidReference)
	}
	return nil
}

func childDirectory(parent, name string, create bool) (string, error) {
	path := filepath.Join(parent, name)
	info, err := os.Lstat(path)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		mkdirErr := os.Mkdir(path, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return "", fmt.Errorf("create run store directory: %w", mkdirErr)
		}
		created = mkdirErr == nil
		info, err = os.Lstat(path)
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrUnsafePath, path)
	}
	if created {
		if err := syncDirectory(parent); err != nil {
			return "", fmt.Errorf("sync run store directory: %w", err)
		}
	}
	return path, nil
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnsafePath, path)
	}
	return nil
}

func verifyFile(path, digest string) error {
	return withRegularFile(path, func(file *os.File) error {
		return verifyDigest(file, digest)
	})
}

func readVerifiedFile(path, digest string) ([]byte, error) {
	var data []byte
	err := withRegularFile(path, func(file *os.File) error {
		var err error
		data, err = io.ReadAll(file)
		if err != nil {
			return fmt.Errorf("read published artifact: %w", err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("%w: %s", ErrReferenceIntegrity, path)
		}
		return nil
	})
	return data, err
}

// withRegularFile opens a single regular handle after rejecting a path-level
// link. Consumers hash or read that handle directly, so they cannot validate
// one file and consume another after a path replacement.
func withRegularFile(path string, consume func(*os.File) error) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrReferenceUnavailable, path)
	}
	if err != nil {
		return fmt.Errorf("inspect published artifact: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: artifact is not a regular file", ErrUnsafePath)
	}
	if beforeArtifactOpenHook != nil {
		beforeArtifactOpenHook(path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open published artifact: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat opened artifact: %w", err)
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%w: opened artifact is not a regular file", ErrUnsafePath)
	}
	return consume(file)
}

func verifyDigest(source io.Reader, digest string) error {
	hash := sha256.New()
	buffer := make([]byte, verificationBuffer)
	if _, err := io.CopyBuffer(hash, source, buffer); err != nil {
		return fmt.Errorf("hash published artifact: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("%w", ErrReferenceIntegrity)
	}
	return nil
}
