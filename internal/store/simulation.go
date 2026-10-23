// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package store

import (
	"os"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
)

type SimulationStore struct{ io *IO }

func NewSimulationStore(io *IO) *SimulationStore { return &SimulationStore{io: io} }

func (s *SimulationStore) Load() (*domain.SimulationProfile, error) {
	var profile domain.SimulationProfile
	if err := s.io.ReadJSON("meta/simulation_profile.json", &profile); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

func (s *SimulationStore) Save(profile domain.SimulationProfile) error {
	if profile.Version == "" {
		profile.Version = domain.SimulationProfileVersion
	}
	if err := domain.ValidateSimulationProfile(&profile); err != nil {
		return err
	}
	return s.io.WriteJSON("meta/simulation_profile.json", profile)
}

// DeleteProfile invalidates the derived simulation profile without touching the
// source corpus. Source uploads and deletions use this so a later analysis cannot
// accidentally reuse a profile built from files that are no longer present.
func (s *SimulationStore) DeleteProfile() error {
	return s.io.RemoveFile("meta/simulation_profile.json")
}
