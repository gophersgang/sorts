// Copyright 2014-5 Randall Farmer. All rights reserved.

// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package radixsort

import (
	"bytes"
	"sync"
)

// radix is how many bits we look at at once in a numeric sort. Particular
// sorts seem to benefit from higher radices, but that's hard to take
// advantage of.  If you're tuning that hard, look beyond this package.
const radix = 8
const mask = (1 << radix) - 1

// qSortCutoff is when we bail out to a quicksort. It's changed to 1 for
// certain tests so we can more easily exercise the radix sorting.  This was
// around the break-even point in some sloppy tests.
var qSortCutoff = 1 << 7

// maxByteSkip controls how long a common prefix the []byte/string sort can
// detect in each pass.  The common prefix check helps a lot on data that
// has them, but we need a limit to bound the worst case (where *almost* all
// data in every pass has a common prefix, and we run into the exception(s)
// late).  Usually, it's a wash.
const maxByteSkip = 16

const keyPanicMessage = "sort failed: Key and Less aren't consistent with each other"
const keyNumberHelp = " (the [NumberType]Key functions like IntKey may help resolve this)"
const panicMessage = "sort failed: could be a data race, a radixsort bug, or a subtle bug in the interface implementation"

// maxRadixDepth limits how deeply the radix part of string sorts can
// recurse before we bail to quicksort.  On 64-bit, radix sort uses 2KB temp
// space always + 2KB per recursion, plus what quicksort uses, so setting it
// to 32 allows eating up to 66KB.
const maxRadixDepth = 32

// byteTblPool is a pool of count/offset tables.
type byteTbl *[256]int

var byteTblPool = sync.Pool{New: func() interface{} { return byteTbl(new([256]int)) }}

var zeroByteTbl = [256]int{}

// ByNumber sorts data by a numeric key.
func ByNumber(data NumberInterface) {
	l := data.Len()
	radixSortUint64(data, guessIntShift(data), 0, l)

	// check results!
	for i := 1; i < l; i++ {
		if data.Less(i, i-1) {
			if data.Key(i) > data.Key(i-1) {
				panic(keyPanicMessage + keyNumberHelp)
			}
			panic(panicMessage)
		}
	}
}

// ByString sorts data by a string key.
func ByString(data StringInterface) {
	bucketStarts := byteTblPool.Get().(byteTbl)
	defer byteTblPool.Put(bucketStarts)
	l := data.Len()
	radixSortString(data, 0, 0, l, 0, bucketStarts)

	// check results!
	for i := 1; i < l; i++ {
		if data.Less(i, i-1) {
			if data.Key(i) > data.Key(i-1) {
				panic(keyPanicMessage)
			}
			panic(panicMessage)
		}
	}
}

// ByBytes sorts data by a []byte key.
func ByBytes(data BytesInterface) {
	bucketStarts := byteTblPool.Get().(byteTbl)
	defer byteTblPool.Put(bucketStarts)
	l := data.Len()
	radixSortBytes(data, 0, 0, l, 0, bucketStarts)

	// check results!
	for i := 1; i < l; i++ {
		if data.Less(i, i-1) {
			if bytes.Compare(data.Key(i), data.Key(i-1)) > 0 {
				panic(keyPanicMessage)
			}
			panic(panicMessage)
		}
	}
}

// guessIntShift saves a pass when the data is distributed roughly uniformly
// in a small range (think shuffled indices into an array), and rarely hurts
// much otherwise: either it just returns 64-radix quickly, or it returns an
// incorrect low value and the sorter recovers after one useless counting
// pass.
func guessIntShift(data NumberInterface) uint {
	l := data.Len()
	if l < qSortCutoff {
		return 64 - radix
	}
	step := l >> 5
	if step == 0 { // only for tests w/qSortCutoff lowered
		step = 1
	}
	min := data.Key(l - 1)
	max := min
	for i := 0; i < l; i += step {
		k := data.Key(i)
		if k < min {
			min = k
		}
		if k > max {
			max = k
		}
	}
	diff := min ^ max
	log2diff := 0
	for diff != 0 {
		log2diff++
		diff >>= 1
	}
	if log2diff < 64 {
		// assuming a uniform distro, it wouldn't be that rare to
		// estimate 1 bit low, so add margin for that
		log2diff++
	}
	shiftGuess := log2diff - radix
	if shiftGuess < 0 {
		return 0
	}
	return uint(shiftGuess)
}

/*
Thanks to (and please refer to):

Victor J. Duvanenko, "Parallel In-Place Radix Sort Simplified", 2011, at
http://www.drdobbs.com/parallel/parallel-in-place-radix-sort-simplified/229000734
for lots of practical discussion of performance

Michael Herf, "Radix Tricks", 2001, at
http://stereopsis.com/radix.html
for the idea for Float32Key()/Float64Key() (via Pierre Tardiman, "Radix Sort
Revisited", 2000, at http://codercorner.com/RadixSortRevisited.htm) and more
performance talk.

A handy slide deck (if it works, it works) summarizing Robert Sedgewick and
Kevin Wayne's Algorithms on string sorts:
http://algs4.cs.princeton.edu/lectures/51StringSorts.pdf
for a grounding in string sorts and pointer to American flag sort

Bentley, McIlroy, and Bostic, "Engineering Radix Sort", 1993 at
http://citeseerx.ist.psu.edu/viewdoc/summary?doi=10.1.1.22.6990
for laying out American flag sort

I've tried some variations on string sort:

- Bentley, Bostic, and McIlroy's trick of keeping our own stack of sorts to
  do instead of recursing.  (Then you only push a sort task for buckets with
  >1 item, whereas now we're eating an int of stack space for every empty
  bucket in every pass.)  Worked, didn't affect run time on test data, made
  code trickier; took it out.  Stack use is already bounded, and some KBs of
  extra stack space are maybe not too costly in 2015 (especially vs. 1993).
  May revisit.

- Keeping both count arrays on the heap using a sync.Pool to recycle them.
  Not much code, but expanding the stack versus expanding a Pool seems like
  a small difference.

- Once the subarray gets small enough that we can afford temp space per
  item, collecting the next 4 or 8 bytes of a string and sorting those, only
  comparing full strings to break ties.  The hope is that the better
  cache-friendliness of sorting that data will outweigh the overhead to set
  it up.

Two other algorithms:

- Radix quicksort: see the Algorithms slide deck and Bentley and Sedgewick,
  "Sorting Strings with Three-Way Radix Quicksort", 1998, at
  http://www.drdobbs.com/database/sorting-strings-with-three-way-radix-qui/184410724
  For us I fear the extra Key calls would outweigh the better
  cache-friendliness of the swaps, but one way to know.

- LSD radix sort for smaller subarrays; not clear what interface that'd use,
  since it isn't in-place.

*/

// All three radixSort functions below do a single pass the data. They all
// fall back to comparison sort for small buckets and equal keys, and try
// to skip prefixes/infixes that are common across the whole (sub)array.

func radixSortUint64(data NumberInterface, shift uint, a, b int) {
	if b-a < qSortCutoff {
		qSort(data, a, b)
		return
	}

	// use a single pass over the keys to bucket data and find min/max
	// (for skipping over long common prefixes)
	var bucketStarts, bucketEnds [1 << radix]int
	min := data.Key(a)
	max := min
	for i := a; i < b; i++ {
		k := data.Key(i)
		bucketStarts[(k>>shift)&mask]++
		if k < min {
			min = k
		}
		if k > max {
			max = k
		}
	}

	// skip past common prefixes, bail if all keys equal
	diff := min ^ max
	if diff == 0 {
		qSort(data, a, b)
		return
	}
	if diff>>shift == 0 || diff>>(shift+radix) != 0 {
		// find highest 1 bit in diff
		log2diff := 0
		for diff != 0 {
			log2diff++
			diff >>= 1
		}
		nextShift := log2diff - radix
		if nextShift < 0 {
			nextShift = 0
		}
		radixSortUint64(data, uint(nextShift), a, b)
		return
	}

	pos := a
	for i, c := range bucketStarts {
		bucketStarts[i] = pos
		pos += c
		bucketEnds[i] = pos
	}

	for curBucket, bucketEnd := range bucketEnds {
		i := bucketStarts[curBucket]
		for i < bucketEnd {
			destBucket := (data.Key(i) >> shift) & mask
			if destBucket == uint64(curBucket) {
				i++
				bucketStarts[destBucket]++
				continue
			}
			data.Swap(i, bucketStarts[destBucket])
			bucketStarts[destBucket]++
		}
	}

	if shift == 0 {
		// each bucket is a unique key; just qSort any dupes
		for _, end := range bucketEnds {
			if end > pos+1 {
				qSort(data, pos, end)
			}
			pos = end
		}
		return
	}

	nextShift := shift - radix
	if shift < radix {
		nextShift = 0
	}
	pos = a
	for _, end := range bucketEnds {
		if end > pos+1 {
			radixSortUint64(data, nextShift, pos, end)
		}
		pos = end
	}
}

func radixSortString(data StringInterface, offset, a, b, depth int, bucketEnds byteTbl) {
	if b-a < qSortCutoff || depth == maxRadixDepth {
		qSort(data, a, b)
		return
	}

	// use a single pass over the keys to swap too-short strings to
	// start, count bucket sizes, and catch if all keys share a prefix
	bucketStarts := [256]int{}
	prefix, prefixIsSet := "", false
	aStart := a
	for i := a; i < b; i++ {
		// swap too-short strings to start
		k := data.Key(i)
		if len(k) <= offset {
			data.Swap(a, i)
			a++
			continue
		}
		k = k[offset:]
		bucketStarts[k[0]]++

		if !prefixIsSet {
			prefix = k
			if len(prefix) > maxByteSkip {
				prefix = prefix[:maxByteSkip]
			}
			prefixIsSet = true
		} else if len(prefix) > 0 {
			if len(k) < len(prefix) {
				prefix = prefix[:len(k)]
			}
			for j := 0; j < len(prefix); j++ {
				if prefix[j] != k[j] {
					prefix = prefix[:j]
					break
				}
			}
		}
	}

	// qSort any strings that were too short
	if a-aStart > 1 {
		qSort(data, aStart, a)
	}

	// skip past common prefixes
	if len(prefix) > 0 {
		radixSortString(data, offset+len(prefix), a, b, depth+1, bucketEnds)
		return
	}

	pos := a
	for i, c := range bucketStarts {
		bucketStarts[i] = pos
		pos += c
		bucketEnds[i] = pos
	}

	for curBucket, bucketEnd := range bucketEnds {
		i := bucketStarts[curBucket]
		for i < bucketEnd {
			destBucket := data.Key(i)[offset]
			if destBucket == byte(curBucket) {
				i++
				bucketStarts[destBucket]++
				continue
			}
			data.Swap(i, bucketStarts[destBucket])
			bucketStarts[destBucket]++
		}
	}

	pos = a
	for _, end := range bucketStarts {
		if end > pos+1 {
			radixSortString(data, offset+1, pos, end, depth+1, bucketEnds)
		}
		pos = end
	}
}

func radixSortBytes(data BytesInterface, offset, a, b, depth int, bucketEnds byteTbl) {
	if b-a < qSortCutoff || depth == maxRadixDepth {
		qSort(data, a, b)
		return
	}

	// use a single pass over the keys to swap too-short strings to
	// start, bucket strings, and catch if all keys share a prefix
	bucketStarts := [256]int{}
	prefix, prefixIsSet := []byte(nil), false
	aStart := a
	for i := a; i < b; i++ {
		// swap too-short strings to start
		k := data.Key(i)
		if len(k) <= offset {
			data.Swap(a, i)
			a++
			continue
		}
		k = k[offset:]
		bucketStarts[k[0]]++

		if !prefixIsSet {
			prefix = k
			if len(prefix) > maxByteSkip {
				prefix = prefix[:maxByteSkip]
			}
			prefixIsSet = true
		} else if len(prefix) > 0 {
			if len(k) < len(prefix) {
				prefix = prefix[:len(k)]
			}
			for j := 0; j < len(prefix); j++ {
				if prefix[j] != k[j] {
					prefix = prefix[:j]
					break
				}
			}
		}
	}

	// qSort any strings that were too short
	if a-aStart > 1 {
		qSort(data, aStart, a)
	}

	// skip past common prefixes
	if len(prefix) > 0 {
		radixSortBytes(data, offset+len(prefix), a, b, depth+1, bucketEnds)
		return
	}

	pos := a
	for i, c := range bucketStarts {
		bucketStarts[i] = pos
		pos += c
		bucketEnds[i] = pos
	}

	for curBucket, bucketEnd := range bucketEnds {
		i := bucketStarts[curBucket]
		for i < bucketEnd {
			destBucket := data.Key(i)[offset]
			if destBucket == byte(curBucket) {
				i++
				bucketStarts[destBucket]++
				continue
			}
			data.Swap(i, bucketStarts[destBucket])
			bucketStarts[destBucket]++
		}
	}

	pos = a
	for _, end := range bucketStarts {
		if end > pos+1 {
			radixSortBytes(data, offset+1, pos, end, depth+1, bucketEnds)
		}
		pos = end
	}
}