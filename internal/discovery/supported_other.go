//go:build !linux && !darwin

package discovery

func Supported() bool {
	return false
}
