package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// listingLocationForm reads a listing's place from a create/update form. The
// app sends country/state/city/area alongside the "location" display string;
// an older app sends only "location", which is parsed (a string with no comma
// stays text only). ok=false means the form says nothing about location.
func listingLocationForm(c *gin.Context) (loc models.ListingLocation, display string, ok bool) {
	display = strings.TrimSpace(c.PostForm("location"))
	loc = models.ListingLocation{
		Country: c.PostForm("country"),
		State:   c.PostForm("state"),
		City:    c.PostForm("city"),
		Area:    c.PostForm("area"),
	}
	if strings.TrimSpace(loc.Country+loc.State+loc.City+loc.Area) != "" {
		return loc, display, true
	}
	if display == "" {
		return models.ListingLocation{}, "", false
	}
	parsed, _ := models.ParseListingLocation(display)
	return parsed, display, true
}

// applyLocationFilter narrows a listing query to the country/state/city the
// caller picked (matched on their LocationKey, so case and a " State" suffix
// don't matter) and to an area, a substring of the listing's area or its
// display location — older listings typed their neighbourhood into the city.
func applyLocationFilter(c *gin.Context, query *gorm.DB) *gorm.DB {
	for param, column := range map[string]string{"country": "country_key", "state": "state_key", "city": "city_key"} {
		if key := models.LocationKey(c.Query(param)); key != "" {
			query = query.Where(column+" = ?", key)
		}
	}
	if area := strings.ToLower(strings.TrimSpace(c.Query("area"))); area != "" {
		pattern := "%" + services.LikeLiteral(area) + "%"
		query = query.Where(`(LOWER(area) LIKE ? ESCAPE '\' OR LOWER(location) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	return query
}
