package goal

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ilocn/stint/internal/idgen"
	"github.com/ilocn/stint/internal/workspace"
)

// ErrDuplicate is returned by Create when a goal with the same text already exists.
var ErrDuplicate = errors.New("duplicate goal")

// Status constants.
const (
	StatusQueued   = "queued"
	StatusPlanning = "planning"
	StatusActive   = "active"
	StatusDone     = "done"
	StatusFailed   = "failed"
)

// Goal represents a user-submitted natural-language objective.
type Goal struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Hints     []string `json:"hints,omitempty"`
	Repos     []string `json:"repos,omitempty"`
	Status    string   `json:"status"`
	Branch    string   `json:"branch,omitempty"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// NewID returns a unique time-sortable goal ID like "g-00001abc123".
func NewID() string {
	return idgen.NewTimeSortableID("g")
}

// Create writes a new queued goal to the workspace.
func Create(ws *workspace.Workspace, text string, hints []string, repos []string) (*Goal, error) {
	// Dedup: if a goal with the same text already exists, return it.
	existing, err := List(ws)
	if err != nil {
		return nil, err
	}
	for _, g := range existing {
		if g.Text == text {
			return g, ErrDuplicate
		}
	}
	now := time.Now().UnixNano()
	g := &Goal{
		ID:        NewID(),
		Text:      text,
		Hints:     hints,
		Repos:     repos,
		Status:    StatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return g, write(ws, g)
}

// Get reads a goal by ID.
func Get(ws *workspace.Workspace, id string) (*Goal, error) {
	data, err := os.ReadFile(ws.GoalPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("goal %s not found", id)
		}
		return nil, err
	}
	var g Goal
	return &g, json.Unmarshal(data, &g)
}

// List returns all goals sorted by creation time.
func List(ws *workspace.Workspace) ([]*Goal, error) {
	entries, err := os.ReadDir(ws.GoalsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var goals []*Goal
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		g, err := Get(ws, id)
		if err != nil {
			slog.Warn("skipping unreadable goal", slog.String("goal_id", id), slog.Any("error", err))
			continue
		}
		goals = append(goals, g)
	}
	sort.Slice(goals, func(i, j int) bool {
		return goals[i].CreatedAt < goals[j].CreatedAt
	})
	return goals, nil
}

// UpdateStatus changes the goal's status and persists it.
func UpdateStatus(ws *workspace.Workspace, id, status string) error {
	g, err := Get(ws, id)
	if err != nil {
		return err
	}
	g.Status = status
	g.UpdatedAt = time.Now().UnixNano()
	return write(ws, g)
}

// SetBranch records the goal's integration branch.
func SetBranch(ws *workspace.Workspace, id, branch string) error {
	g, err := Get(ws, id)
	if err != nil {
		return err
	}
	g.Branch = branch
	g.UpdatedAt = time.Now().UnixNano()
	return write(ws, g)
}

// ActiveRepos returns the set of repos used by goals in planning or active status.
func ActiveRepos(ws *workspace.Workspace) (map[string]bool, error) {
	goals, err := List(ws)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, g := range goals {
		if g.Status == StatusPlanning || g.Status == StatusActive {
			for _, r := range g.Repos {
				active[r] = true
			}
		}
	}
	return active, nil
}

// write atomically persists a goal to disk.
func write(ws *workspace.Workspace, g *Goal) error {
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	tmp := ws.GoalPath(g.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, ws.GoalPath(g.ID))
}
