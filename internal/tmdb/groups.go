package tmdb

import (
	"net/url"
	"strconv"
)

type EpisodeGroupInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         int    `json:"type"`
	GroupCount   int    `json:"group_count"`
	EpisodeCount int    `json:"episode_count"`
}

type EpisodeGroup struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   int    `json:"type"`
	Groups []struct {
		Name     string         `json:"name"`
		Order    int            `json:"order"`
		Episodes []GroupEpisode `json:"episodes"`
	} `json:"groups"`
}

type GroupEpisode struct {
	Episode     // season/episode numbers here are the aired ones
	Order   int `json:"order"`
}

func (t *Client) EpisodeGroups(show int) ([]EpisodeGroupInfo, error) {
	var r struct {
		Results []EpisodeGroupInfo `json:"results"`
	}
	err := t.get("/tv/"+strconv.Itoa(show)+"/episode_groups", nil, &r)
	return r.Results, err
}

func (t *Client) EpisodeGroup(id string) (*EpisodeGroup, error) {
	var g EpisodeGroup
	if err := t.get("/tv/episode_group/"+url.PathEscape(id), nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}
