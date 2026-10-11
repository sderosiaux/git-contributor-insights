package ownership

import (
	"bytes"
	"fmt"
	"strings"
)

// A first-parent change anchor is deliberately more conservative than a blob
// cache: edits, reverts, renames and merge changes all create new anchors. A
// path untouched since an anchor has the same blame along that first-parent
// lineage. Ordinary blame still follows all parents at the selected revision.
func (e *engine) readFirstParentChanges(head string) error {
	output, err := e.git("log", "-z", "--format=%x00%H", "--name-only", "--no-renames", "--diff-merges=first-parent", "--no-ext-diff", "--no-textconv", head)
	if err != nil {
		return err
	}
	e.firstParentChanges = map[string][]string{}
	sha := ""
	header, firstPath := false, false
	for _, token := range bytes.Split(output, []byte{0}) {
		if len(token) == 0 {
			header = true
			continue
		}
		if header {
			sha = string(token)
			if _, exists := e.commits[sha]; !exists {
				return fmt.Errorf("invalid first-parent change header")
			}
			e.firstParentOrder = append(e.firstParentOrder, sha)
			header, firstPath = false, true
			continue
		}
		filename := string(token)
		if firstPath {
			filename = strings.TrimPrefix(filename, "\n")
			firstPath = false
		}
		if filename != "" {
			e.firstParentChanges[sha] = append(e.firstParentChanges[sha], filename)
		}
	}
	return nil
}

func (e *engine) blameAnchors(revision string) map[string]string {
	if anchors, exists := e.anchors[revision]; exists {
		return anchors
	}
	anchors := map[string]string{}
	for sha := revision; sha != ""; {
		for _, filename := range e.firstParentChanges[sha] {
			if _, exists := anchors[filename]; !exists {
				anchors[filename] = sha
			}
		}
		parents := e.commits[sha].parents
		if len(parents) == 0 {
			break
		}
		sha = parents[0]
	}
	e.anchors[revision] = anchors
	if len(e.anchors) > 4 {
		for sha := range e.anchors {
			if sha != revision {
				delete(e.anchors, sha)
				break
			}
		}
	}
	return anchors
}
