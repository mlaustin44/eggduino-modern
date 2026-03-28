package main

import "math"

// Point represents a 2D coordinate.
type Point struct {
	X, Y float64
}

func dist(a, b Point) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// Polyline is a sequence of points forming a path.
type Polyline []Point

func (p Polyline) start() Point { return p[0] }
func (p Polyline) end() Point   { return p[len(p)-1] }
func (p Polyline) length() float64 {
	total := 0.0
	for i := 1; i < len(p); i++ {
		total += dist(p[i-1], p[i])
	}
	return total
}

func reversePolyline(p Polyline) Polyline {
	r := make(Polyline, len(p))
	for i, pt := range p {
		r[len(p)-1-i] = pt
	}
	return r
}

// MergePaths joins paths whose endpoints are within tolerance.
// Tries both appending and prepending, with optional line reversal.
func MergePaths(paths []Polyline, tolerance float64) []Polyline {
	if len(paths) == 0 {
		return paths
	}

	available := make([]bool, len(paths))
	for i := range available {
		available[i] = true
	}

	var result []Polyline

	for {
		// Find next available path to start a chain
		seedIdx := -1
		for i, a := range available {
			if a {
				seedIdx = i
				break
			}
		}
		if seedIdx < 0 {
			break
		}

		available[seedIdx] = false
		current := make(Polyline, len(paths[seedIdx]))
		copy(current, paths[seedIdx])

		// Repeatedly try to extend the current chain
		for {
			extended := false

			// Try to append: find a path whose start or end is near current's end
			bestIdx := -1
			bestDist := tolerance
			bestReverse := false
			curEnd := current.end()

			for i, a := range available {
				if !a {
					continue
				}
				d := dist(curEnd, paths[i].start())
				if d < bestDist {
					bestDist = d
					bestIdx = i
					bestReverse = false
				}
				d = dist(curEnd, paths[i].end())
				if d < bestDist {
					bestDist = d
					bestIdx = i
					bestReverse = true
				}
			}

			if bestIdx >= 0 {
				available[bestIdx] = false
				toAppend := paths[bestIdx]
				if bestReverse {
					toAppend = reversePolyline(toAppend)
				}
				// Skip the first point of the appended path (it's ~same as current end)
				current = append(current, toAppend[1:]...)
				extended = true
				continue // Try to extend further
			}

			// Try to prepend: find a path whose start or end is near current's start
			bestIdx = -1
			bestDist = tolerance
			bestReverse = false
			curStart := current.start()

			for i, a := range available {
				if !a {
					continue
				}
				d := dist(curStart, paths[i].end())
				if d < bestDist {
					bestDist = d
					bestIdx = i
					bestReverse = false
				}
				d = dist(curStart, paths[i].start())
				if d < bestDist {
					bestDist = d
					bestIdx = i
					bestReverse = true
				}
			}

			if bestIdx >= 0 {
				available[bestIdx] = false
				toPrepend := paths[bestIdx]
				if bestReverse {
					toPrepend = reversePolyline(toPrepend)
				}
				// Prepend: toPrepend's end ≈ current's start
				newPath := make(Polyline, 0, len(toPrepend)+len(current)-1)
				newPath = append(newPath, toPrepend...)
				newPath = append(newPath, current[1:]...)
				current = newPath
				extended = true
				continue
			}

			if !extended {
				break
			}
		}

		if len(current) > 1 {
			result = append(result, current)
		}
	}

	return result
}

// ReorderPaths sorts paths using nearest-neighbor to minimize pen-up travel.
// Paths may be reversed if the end is closer than the start.
func ReorderPaths(paths []Polyline) []Polyline {
	if len(paths) <= 1 {
		return paths
	}

	available := make([]bool, len(paths))
	for i := range available {
		available[i] = true
	}

	result := make([]Polyline, 0, len(paths))
	cur := Point{0, 0} // Start from origin

	for len(result) < len(paths) {
		bestIdx := -1
		bestDist := math.MaxFloat64
		bestReverse := false

		for i, a := range available {
			if !a {
				continue
			}
			// Distance to start
			d := dist(cur, paths[i].start())
			if d < bestDist {
				bestDist = d
				bestIdx = i
				bestReverse = false
			}
			// Distance to end (would reverse the path)
			d = dist(cur, paths[i].end())
			if d < bestDist {
				bestDist = d
				bestIdx = i
				bestReverse = true
			}
		}

		if bestIdx < 0 {
			break
		}

		available[bestIdx] = false
		p := paths[bestIdx]
		if bestReverse {
			p = reversePolyline(p)
		}
		result = append(result, p)
		cur = p.end()
	}

	return result
}

// SimplifyPolyline reduces points using the Ramer-Douglas-Peucker algorithm.
func SimplifyPolyline(p Polyline, epsilon float64) Polyline {
	if len(p) <= 2 {
		return p
	}

	// Find the point with maximum distance from the line between first and last
	maxDist := 0.0
	maxIdx := 0
	start, end := p[0], p[len(p)-1]

	for i := 1; i < len(p)-1; i++ {
		d := pointLineDistance(p[i], start, end)
		if d > maxDist {
			maxDist = d
			maxIdx = i
		}
	}

	if maxDist > epsilon {
		// Recurse on both halves
		left := SimplifyPolyline(p[:maxIdx+1], epsilon)
		right := SimplifyPolyline(p[maxIdx:], epsilon)
		// Join, avoiding duplicate at maxIdx
		result := make(Polyline, 0, len(left)+len(right)-1)
		result = append(result, left...)
		result = append(result, right[1:]...)
		return result
	}

	// All points are within epsilon — just keep endpoints
	return Polyline{start, end}
}

func pointLineDistance(p, lineStart, lineEnd Point) float64 {
	dx := lineEnd.X - lineStart.X
	dy := lineEnd.Y - lineStart.Y
	lenSq := dx*dx + dy*dy

	if lenSq == 0 {
		return dist(p, lineStart)
	}

	t := ((p.X-lineStart.X)*dx + (p.Y-lineStart.Y)*dy) / lenSq
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	proj := Point{
		X: lineStart.X + t*dx,
		Y: lineStart.Y + t*dy,
	}
	return dist(p, proj)
}

// ElideShort removes paths shorter than minLength.
func ElideShort(paths []Polyline, minLength float64) []Polyline {
	result := make([]Polyline, 0, len(paths))
	for _, p := range paths {
		if p.length() >= minLength {
			result = append(result, p)
		}
	}
	return result
}

// OptimizePaths runs the full optimization pipeline.
func OptimizePaths(paths []Polyline, mergeTolerance, simplifyEpsilon, minLength float64) []Polyline {
	// 1. Merge paths with close endpoints
	paths = MergePaths(paths, mergeTolerance)

	// 2. Simplify each path (reduce point count)
	if simplifyEpsilon > 0 {
		for i, p := range paths {
			paths[i] = SimplifyPolyline(p, simplifyEpsilon)
		}
	}

	// 3. Remove very short paths
	if minLength > 0 {
		paths = ElideShort(paths, minLength)
	}

	// 4. Reorder for minimum pen-up travel
	paths = ReorderPaths(paths)

	return paths
}
