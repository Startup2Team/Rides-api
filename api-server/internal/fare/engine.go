package fare

import "time"

type Config struct {
	ID                 string  `json:"id"`
	VehicleTypeCode    string  `json:"vehicle_type_code"`
	BaseFareRWF        int     `json:"base_fare_rwf"`
	BaseDistanceKM     float64 `json:"base_distance_km"`
	Tier1PerKmRWF      int     `json:"tier1_per_km_rwf"`
	Tier1MaxKM         float64 `json:"tier1_max_km"`
	Tier2PerKmRWF      int     `json:"tier2_per_km_rwf"`
	NightSurchargePct  float64 `json:"night_surcharge_pct"`
	NightStartHour     int     `json:"night_start_hour"`
	NightEndHour       int     `json:"night_end_hour"`
	WaitingRWFPerMin   float64 `json:"waiting_rwf_per_min"`
	WaitingFreeMinutes int     `json:"waiting_free_minutes"`
	MinFareRWF         int     `json:"min_fare_rwf"`
	CancellationFeeRWF int     `json:"cancellation_fee_rwf"`
	IsActive           bool    `json:"is_active"`
	EffectiveFrom      string  `json:"effective_from,omitempty"`
}

type Breakdown struct {
	BaseFare           float64 `json:"base_fare_rwf"`
	DistanceCharge     float64 `json:"distance_charge_rwf"`
	NightSurcharge     float64 `json:"night_surcharge_rwf"`
	WaitingCharge      float64 `json:"waiting_charge_rwf"`
	SubtotalBeforeMin  float64 `json:"subtotal_before_min_rwf"`
	TotalFare          float64 `json:"total_fare_rwf"`
	NightApplied       bool    `json:"night_surcharge_applied"`
	WaitingSeconds     int     `json:"waiting_seconds"`
	FreeWaitingSeconds int     `json:"free_waiting_seconds"`
}

func Calculate(cfg *Config, distanceKM float64, startedAt time.Time, waitingSeconds int) Breakdown {
	base := float64(cfg.BaseFareRWF)
	distCharge := 0.0

	if distanceKM > cfg.BaseDistanceKM {
		billableKM := distanceKM - cfg.BaseDistanceKM
		tier1Cap := cfg.Tier1MaxKM - cfg.BaseDistanceKM
		if billableKM <= tier1Cap {
			distCharge = billableKM * float64(cfg.Tier1PerKmRWF)
		} else {
			distCharge = tier1Cap*float64(cfg.Tier1PerKmRWF) + (billableKM-tier1Cap)*float64(cfg.Tier2PerKmRWF)
		}
	}

	subtotal := base + distCharge
	nightApplied := isNight(startedAt, cfg.NightStartHour, cfg.NightEndHour)
	nightCharge := 0.0
	if nightApplied && cfg.NightSurchargePct > 0 {
		nightCharge = subtotal * cfg.NightSurchargePct
		subtotal += nightCharge
	}

	subtotalBeforeMin := subtotal
	if subtotal < float64(cfg.MinFareRWF) {
		subtotal = float64(cfg.MinFareRWF)
	}

	freeSeconds := cfg.WaitingFreeMinutes * 60
	billableWaitSeconds := waitingSeconds - freeSeconds
	if billableWaitSeconds < 0 {
		billableWaitSeconds = 0
	}
	waitCharge := (float64(billableWaitSeconds) / 60.0) * cfg.WaitingRWFPerMin

	total := subtotal + waitCharge
	return Breakdown{
		BaseFare:           base,
		DistanceCharge:     distCharge,
		NightSurcharge:     nightCharge,
		WaitingCharge:      waitCharge,
		SubtotalBeforeMin:  subtotalBeforeMin,
		TotalFare:          total,
		NightApplied:       nightApplied,
		WaitingSeconds:     waitingSeconds,
		FreeWaitingSeconds: freeSeconds,
	}
}

func isNight(t time.Time, startHour, endHour int) bool {
	h := t.Hour()
	if startHour > endHour {
		return h >= startHour || h < endHour
	}
	return h >= startHour && h < endHour
}

func CancellationFee(cfg *Config, driverHasArrived bool) float64 {
	if !driverHasArrived {
		return 0
	}
	return float64(cfg.CancellationFeeRWF)
}
