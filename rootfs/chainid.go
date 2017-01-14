package rootfs

import digest "github.com/opencontainers/go-digest"

// NOTE(stevvooe): These are copy-pasted from the opencontainers/image-spec
// project. Once https://github.com/opencontainers/image-spec/pull/486 is
// merged, we can use those from the upstream.

// ChainID takes a slice of digests and returns the ChainID corresponding to
// the last entry. Typically, these are a list of layer DiffIDs, with the
// result providing the ChainID identifying the result of sequential
// application of the preceding layers.
func ChainID(dgsts []digest.Digest) digest.Digest {
	chainIDs := make([]digest.Digest, len(dgsts))
	copy(chainIDs, dgsts)
	ChainIDs(chainIDs)

	if len(chainIDs) == 0 {
		return ""
	}
	return chainIDs[len(chainIDs)-1]
}

// ChainIDs calculates the recursively applied chain id for each identifier in
// the slice. The result is written direcly back into the slice such that the
// ChainID for each item will be in the respective position.
//
// By definition of ChainID, the zeroth element will always be the same before
// and after the call.
//
// As an example, given the chain of ids `[A, B, C]`, the result `[A,
// ChainID(A|B), ChainID(A|B|C)]` will be written back to the slice.
//
// The input is provided as a return value for convenience.
//
// Typically, these are a list of layer DiffIDs, with the
// result providing the ChainID for each the result of each layer application
// sequentially.
func ChainIDs(dgsts []digest.Digest) []digest.Digest {
	if len(dgsts) < 2 {
		return dgsts
	}

	parent := digest.FromBytes([]byte(dgsts[0] + " " + dgsts[1]))
	next := dgsts[1:]
	next[0] = parent
	ChainIDs(next)

	return dgsts
}
