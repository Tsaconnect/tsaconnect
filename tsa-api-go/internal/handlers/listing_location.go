package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

// sortNearest is the sortBy that orders listings by closeness to the caller.
const sortNearest = "nearest"

// nearestFirst orders listings by closeness to the place in ?nearCountry,
// ?nearState and ?nearCity (the caller's own; matched on LocationKey): same
// city, then same state, then same country, then the rest — newest first in
// each (id breaks ties, so pages are stable). Given a state, a city counts only
// within it, since city names repeat across states; a city with no state is
// matched on its own. Given no place it is simply newest first.
func nearestFirst(c *gin.Context) clause.OrderBy {
	country := models.LocationKey(c.Query("nearCountry"))
	state := models.LocationKey(c.Query("nearState"))
	city := models.LocationKey(c.Query("nearCity"))

	var when []string
	var vars []interface{}
	rank := 0
	add := func(cond string, v ...interface{}) {
		when = append(when, "WHEN "+cond+" THEN "+string(rune('0'+rank)))
		vars = append(vars, v...)
		rank++
	}
	switch {
	case city != "" && state != "":
		add("city_key = ? AND state_key = ?", city, state)
	case city != "":
		add("city_key = ?", city)
	}
	if state != "" {
		add("state_key = ?", state)
	}
	if country != "" {
		add("country_key = ?", country)
	}
	if len(when) == 0 {
		return clause.OrderBy{Columns: []clause.OrderByColumn{
			{Column: clause.Column{Name: "created_at"}, Desc: true},
			{Column: clause.Column{Name: "id"}, Desc: true},
		}}
	}
	sql := "CASE " + strings.Join(when, " ") + " ELSE " + string(rune('0'+rank)) + " END, created_at DESC, id DESC"
	return clause.OrderBy{Expression: clause.Expr{SQL: sql, Vars: vars, WithoutParentheses: true}}
}
