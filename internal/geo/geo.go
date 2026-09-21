// Package geo maps Datum location names to the city they serve from.
package geo

import (
	"math"
	"strings"
	"sync"
)

// Place is a Datum location and where it physically sits.
type Place struct {
	Name        string  `json:"name"`
	CityCode    string  `json:"cityCode"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
}

// Directory resolves location names, preferring what the Locations API
// reported over the built-in table.
type Directory struct {
	mu   sync.RWMutex
	live map[string]Place
}

// NewDirectory returns a Directory backed only by the built-in table.
func NewDirectory() *Directory { return &Directory{live: map[string]Place{}} }

// Update replaces the places learned from the Locations API.
func (d *Directory) Update(places []Place) {
	m := make(map[string]Place, len(places))
	for _, p := range places {
		m[p.Name] = p
	}
	d.mu.Lock()
	d.live = m
	d.mu.Unlock()
}

// Lookup resolves a location name. Missing fields on a live entry, such as
// coordinates, are filled from the built-in table by name or city code.
func (d *Directory) Lookup(name string) (Place, bool) {
	d.mu.RLock()
	live, haveLive := d.live[name]
	d.mu.RUnlock()

	static, haveStatic := Static(name)
	if !haveStatic && haveLive && live.CityCode != "" {
		static, haveStatic = StaticByCityCode(live.CityCode)
	}
	switch {
	case haveLive && haveStatic:
		return merge(live, static), true
	case haveLive:
		return live, true
	case haveStatic:
		return static, true
	}
	return Place{Name: name, City: name}, false
}

func merge(primary, fallback Place) Place {
	if primary.CityCode == "" {
		primary.CityCode = fallback.CityCode
	}
	if primary.City == "" {
		primary.City = fallback.City
	}
	if primary.Country == "" {
		primary.Country = fallback.Country
	}
	if primary.CountryCode == "" {
		primary.CountryCode = fallback.CountryCode
	}
	if primary.Lat == 0 && primary.Lon == 0 {
		primary.Lat, primary.Lon = fallback.Lat, fallback.Lon
	}
	return primary
}

// Static looks a location up in the built-in table.
func Static(name string) (Place, bool) {
	for _, p := range staticLocations {
		if p.Name == name {
			return p, true
		}
	}
	return Place{}, false
}

// StaticByCityCode looks a city code, such as DFW, up in the built-in table.
func StaticByCityCode(code string) (Place, bool) {
	for _, p := range staticLocations {
		if strings.EqualFold(p.CityCode, code) {
			return p, true
		}
	}
	return Place{}, false
}

// DistanceKm is the great-circle distance between two places.
func DistanceKm(a, b Place) float64 {
	const earthRadiusKm = 6371
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLon := (b.Lon - a.Lon) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(h))
}
