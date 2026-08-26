package platformsupport

import "fmt"

const Requirement = "windows/amd64 or darwin/arm64"

func Validate(goos, goarch string) error {
	if goos == "windows" && goarch == "amd64" || goos == "darwin" && goarch == "arm64" {
		return nil
	}
	return fmt.Errorf("unsupported platform %s/%s: require %s", goos, goarch, Requirement)
}
