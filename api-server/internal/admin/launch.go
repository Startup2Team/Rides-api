package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// Launch readiness: aggregated go-live checklist for the admin console.

func (s *Service) LaunchReadiness(ctx context.Context) (map[string]interface{}, error) {
	filePath := "launch_tracker.json"
	raw, err := os.ReadFile(filePath)
	if err != nil {
		raw, err = os.ReadFile("api-server/launch_tracker.json")
		if err != nil {
			return nil, fmt.Errorf("failed to read launch_tracker.json: %w", err)
		}
	}

	var data LaunchTrackerData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal launch_tracker.json: %w", err)
	}

	totalHours := 0.0
	remainingHours := 0.0
	solomonRem := 0.0
	pacifique_rem := 0.0
	app_team_rem := 0.0

	for _, track := range data.Tracks {
		for _, task := range track.Tasks {
			totalHours += task.EstimateHours
			if task.Status != "VERIFIED" {
				remainingHours += task.EstimateHours
				switch task.Owner {
				case "Solomon":
					solomonRem += task.EstimateHours
				case "Pacifique":
					pacifique_rem += task.EstimateHours
				case "App Team":
					app_team_rem += task.EstimateHours
				}
			}
		}
	}

	totalDays := totalHours / 8.0
	remainingDays := remainingHours / 8.0

	var devMaxHours float64
	if app_team_rem > devMaxHours {
		devMaxHours = app_team_rem
	}
	var solomonDevRem, pacifiqueDevRem float64
	for _, track := range data.Tracks {
		if track.Name == "Testing & QA Tasks" || track.Name == "Launch" {
			continue
		}
		for _, task := range track.Tasks {
			if task.Status != "VERIFIED" {
				if task.Owner == "Solomon" {
					solomonDevRem += task.EstimateHours
				} else if task.Owner == "Pacifique" {
					pacifiqueDevRem += task.EstimateHours
				}
			}
		}
	}
	if solomonDevRem > devMaxHours {
		devMaxHours = solomonDevRem
	}
	if pacifiqueDevRem > devMaxHours {
		devMaxHours = pacifiqueDevRem
	}

	var qaLaunchRem float64
	for _, track := range data.Tracks {
		if track.Name == "Testing & QA Tasks" || track.Name == "Launch" {
			for _, task := range track.Tasks {
				if task.Status != "VERIFIED" {
					qaLaunchRem += task.EstimateHours
				}
			}
		}
	}

	calendarDaysLeft := (devMaxHours / 8.0) + (qaLaunchRem / 8.0)

	progressPct := 0
	if totalHours > 0 {
		progressPct = int(((totalHours - remainingHours) / totalHours) * 100)
	}

	return map[string]interface{}{
		"team":               data.Team,
		"api_endpoint":       data.APIEndpoint,
		"total_hours":        totalHours,
		"total_days":         totalDays,
		"remaining_hours":    remainingHours,
		"remaining_days":     remainingDays,
		"calendar_days_left": calendarDaysLeft,
		"progress_pct":       progressPct,
		"tracks":             data.Tracks,
	}, nil
}
