package main

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func testSpotifyActivity(start, end time.Time) *discordgo.Activity {
	act := &discordgo.Activity{
		Name:    "Spotify",
		Details: "Test song",
		State:   "Test artist",
		Timestamps: discordgo.TimeStamps{
			StartTimestamp: start.UnixMilli(),
		},
	}
	if !end.IsZero() {
		act.Timestamps.EndTimestamp = end.UnixMilli()
	}
	return act
}

func TestSpotifyPresenceStopOverridesOldGuildSnapshot(t *testing.T) {
	const userID = "spotify-stop-test"
	defer latestSpotifyActivities.Delete(userID)
	latestSpotifyActivities.Delete(userID)
	activity := testSpotifyActivity(time.Now().Add(-time.Minute), time.Now().Add(2*time.Minute))
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: "guild", Presences: []*discordgo.Presence{{
		User: &discordgo.User{ID: userID}, Activities: []*discordgo.Activity{activity},
	}}}); err != nil {
		t.Fatal(err)
	}
	session := &discordgo.Session{State: state}

	if got := getSpotifyActivity(session, userID, "guild"); got != activity {
		t.Fatalf("initial state lookup = %v, want current Spotify activity", got)
	}
	cacheSpotifyPresenceUpdate(&discordgo.PresenceUpdate{
		Presence: discordgo.Presence{User: &discordgo.User{ID: userID}}, GuildID: "guild",
	})
	if got := getSpotifyActivity(session, userID, "guild"); got != nil {
		t.Fatalf("stopped activity was resurrected from old guild snapshot: %v", got)
	}

	newActivity := testSpotifyActivity(time.Now(), time.Now().Add(3*time.Minute))
	cacheSpotifyPresenceUpdate(&discordgo.PresenceUpdate{
		Presence: discordgo.Presence{User: &discordgo.User{ID: userID}, Activities: []*discordgo.Activity{newActivity}}, GuildID: "guild",
	})
	if got := getSpotifyActivity(session, userID, "guild"); got != newActivity {
		t.Fatalf("new presence was not picked up: %v", got)
	}
}

func TestSpotifyActivityExpiryDoesNotRefreshStaleSnapshot(t *testing.T) {
	const userID = "spotify-expired-test"
	defer latestSpotifyActivities.Delete(userID)
	oldActivity := testSpotifyActivity(time.Now().Add(-4*time.Minute), time.Now().Add(-time.Minute))
	record := &SpotifyActivityRecord{Activity: oldActivity, UpdatedAt: time.Now().Add(-4 * time.Minute), GuildID: "guild"}
	latestSpotifyActivities.Store(userID, record)
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: "guild", Presences: []*discordgo.Presence{{
		User: &discordgo.User{ID: userID}, Activities: []*discordgo.Activity{oldActivity},
	}}}); err != nil {
		t.Fatal(err)
	}

	if got := getSpotifyActivity(&discordgo.Session{State: state}, userID, "guild"); got != nil {
		t.Fatalf("expired song returned from cache: %v", got)
	}
	if stored, _ := latestSpotifyActivities.Load(userID); stored != record {
		t.Fatal("expired song was recached from stale guild state")
	}
	longActivity := testSpotifyActivity(time.Now().Add(-5*time.Minute), time.Now().Add(5*time.Minute))
	latestSpotifyActivities.Store(userID, &SpotifyActivityRecord{Activity: longActivity, UpdatedAt: time.Now().Add(-5 * time.Minute)})
	if got := getSpotifyActivity(nil, userID, ""); got != longActivity {
		t.Fatalf("ongoing song was discarded only because the presence update was old: %v", got)
	}

	noEnd := testSpotifyActivity(time.Now().Add(-time.Hour), time.Time{})
	latestSpotifyActivities.Store(userID, &SpotifyActivityRecord{Activity: noEnd, UpdatedAt: time.Now().Add(-time.Hour)})
	if got := getSpotifyActivity(nil, userID, ""); got != nil {
		t.Fatalf("old activity without end timestamp returned: %v", got)
	}
}

func TestSpotifyStateCanArriveAfterFirstLookup(t *testing.T) {
	const userID = "spotify-late-state-test"
	defer latestSpotifyActivities.Delete(userID)
	latestSpotifyActivities.Delete(userID)
	state := discordgo.NewState()
	session := &discordgo.Session{State: state}
	if got := getSpotifyActivity(session, userID, "guild"); got != nil {
		t.Fatalf("empty state returned activity: %v", got)
	}
	activity := testSpotifyActivity(time.Now(), time.Now().Add(2*time.Minute))
	if err := state.GuildAdd(&discordgo.Guild{ID: "guild", Presences: []*discordgo.Presence{{
		User: &discordgo.User{ID: userID}, Activities: []*discordgo.Activity{activity},
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := getSpotifyActivity(session, userID, "guild"); got != activity {
		t.Fatalf("late state was not found: %v", got)
	}
}
