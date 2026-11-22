package storage

import (
	"context"
	"fmt"
)

// Driver resolves locations of a single scheme into endpoints.
type Driver interface {
	// ResolveSource resolves a source location. The location may be a
	// single file or a directory.
	ResolveSource(ctx context.Context, loc Location) (Resolved, error)
	// ResolveDest resolves a destination location. The destination is
	// normally a directory. When singleFile is set, the location holds the
	// file that is copied, the way rsync treats the destination of a
	// single file.
	ResolveDest(ctx context.Context, loc Location, singleFile bool) (Resolved, error)
}

// Resolved is a location that has been turned into a usable endpoint.
type Resolved struct {
	// Endpoint is rooted at the directory that holds the location.
	Endpoint Endpoint
	// Name is the base name the location takes under the destination. It
	// is empty when the contents of the location land directly in the
	// destination, which is what a trailing separator selects.
	Name string
	// IsDir reports whether the location is a directory.
	IsDir bool
	// FileKey is the key of a single-file location inside Endpoint.
	FileKey string
	// Display is the location as the user wrote it. It is used in output.
	Display string
}

// Registry maps schemes to the drivers that serve them.
type Registry struct {
	drivers map[Scheme]Driver
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{drivers: make(map[Scheme]Driver)}
}

// Register adds the driver that serves a scheme.
func (r *Registry) Register(scheme Scheme, driver Driver) {
	r.drivers[scheme] = driver
}

// ResolveSource resolves a source location written by the user.
func (r *Registry) ResolveSource(ctx context.Context, raw string) (Resolved, error) {
	loc, err := ParseLocation(raw)
	if err != nil {
		return Resolved{}, err
	}

	driver, ok := r.drivers[loc.Scheme]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, loc.Scheme)
	}

	return driver.ResolveSource(ctx, loc)
}

// ResolveDest resolves a destination location written by the user. When
// singleFile is set, the destination holds the file that is copied.
func (r *Registry) ResolveDest(ctx context.Context, raw string, singleFile bool) (Resolved, error) {
	loc, err := ParseLocation(raw)
	if err != nil {
		return Resolved{}, err
	}

	driver, ok := r.drivers[loc.Scheme]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, loc.Scheme)
	}

	return driver.ResolveDest(ctx, loc, singleFile)
}
