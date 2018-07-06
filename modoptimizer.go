package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sort"

	"github.com/PuerkitoBio/goquery"
)

type Mod struct {
	Uid            string           `json:"uid"`
	Slot           string           `json:"slot"`
	Set            string           `json:"set"`
	Level          int              `json:"level"`
	Pips           int              `json:"pips"`
	TotalScore     int              `json:"totalScore"`
	CharacterName  string           `json:"characterName"`
	PrimaryStat    PrimaryStat      `json:"primaryStat"`
	SecondaryStats []*SecondaryStat `json:"secondaryStats"`
}

type SecondaryScore struct {
	Type string
	Min  float64
	Max  float64
}

type Stat struct {
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

type PrimaryStat struct {
	Stat
}

type SecondaryStat struct {
	Stat
	Score int `json:"score"`
}

var (
	modSlotMap = map[string]string{
		"1": "square",
		"2": "arrow",
		"3": "diamond",
		"4": "triangle",
		"5": "circle",
		"6": "cross",
	}

	modSetMap = map[string]string{
		"1": "health",
		"2": "offense",
		"3": "defense",
		"4": "speed",
		"5": "critchance",
		"6": "critdamage",
		"7": "potency",
		"8": "tenacity",
	}
)

var (
	httpPort = flag.Int("port", 8081, "HTTP port to listen on")
)

func round(x float64) int {
	t := math.Trunc(x)
	if math.Abs(x-t) >= 0.5 {
		return int(t + math.Copysign(1, x))
	}
	return int(t)
}

func parseStat(rawType string, rawValue string) (Stat, error) {
	statValueStr := strings.TrimPrefix(rawValue, "+")
	statType := rawType

	if strings.HasSuffix(statValueStr, "%") {
		statType = fmt.Sprintf("%s %%", rawType)
		statValueStr = strings.TrimSuffix(statValueStr, "%")
	}

	statValue, err := strconv.ParseFloat(statValueStr, 64)

	if err != nil {
		log.Printf("Failed to parse stat value: %s", statValueStr)
		return Stat{}, err
	}

	return Stat{statType, statValue}, nil
}

func getPageCount(user string) (int, error) {
	resp, err := http.Get(fmt.Sprintf("https://swgoh.gg/u/%s/mods/", user))
	if err != nil {
		log.Fatal("Failed to fetch mods: ", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		log.Fatal(err)
	}

	pageText := doc.Find(".pull-right .pagination li a").First().Text()

	log.Printf("Found page text %s", pageText)

	r := regexp.MustCompile("Page [0-9]+ of ([0-9]+)")

	return strconv.Atoi(r.FindStringSubmatch(pageText)[1])
}

func getMods(user string) []*Mod {
	var mods []*Mod
	var secondaryScoreMap = make(map[string]*SecondaryScore)

	modChan := make(chan *Mod)

	pageCount, err := getPageCount(user)

	if err != nil {
		log.Fatal("Failed to get page count", err)
	}

	var wg sync.WaitGroup
	wg.Add(pageCount)

	for i := 1; i < pageCount+1; i++ {
		go func(page int) {
			defer wg.Done()

			resp, err := http.Get(fmt.Sprintf("https://swgoh.gg/u/%s/mods/?page=%d", user, page))
			if err != nil {
				log.Fatal("Failed to fetch mods: ", err)
			}
			defer resp.Body.Close()

			doc, err := goquery.NewDocumentFromReader(resp.Body)

			if err != nil {
				log.Fatal(err)
			}

			r := regexp.MustCompile("statmodmystery_([0-9])_([0-9]).png")

			doc.Find(".collection-mod").Each(func(i int, s *goquery.Selection) {
				modUid, _ := s.Attr("data-id")

				var set string
				var slot string
				if imageSrcAttr, ok := s.Find(".statmod-img").First().Attr("src"); ok {
					set = modSetMap[r.FindStringSubmatch(imageSrcAttr)[1]]
					slot = modSlotMap[r.FindStringSubmatch(imageSrcAttr)[2]]
				}

				pips := s.Find(".statmod-pip").Size()

				level, _ := strconv.Atoi(s.Find(".statmod-level").First().Text())

				character, _ := s.Find(".char-portrait").First().Attr("title")

				primaryStatType := s.Find(".statmod-stats-1 .statmod-stat-label").First().Text()
				primaryStatValueRaw := s.Find(".statmod-stats-1 .statmod-stat-value").First().Text()

				primaryStat, _ := parseStat(primaryStatType, primaryStatValueRaw)

				var secondaryStats []*SecondaryStat

				s.Find(".statmod-stats-2 .statmod-stat").Each(func(i int, statNode *goquery.Selection) {
					secondaryStatType := statNode.Find(".statmod-stat-label").First().Text()
					secondaryStatValueRaw := statNode.Find(".statmod-stat-value").First().Text()

					stat, _ := parseStat(secondaryStatType, secondaryStatValueRaw)

					if level >= 12 && pips >= 4 {
						if val, ok := secondaryScoreMap[stat.Type]; ok {
							val.Max = math.Max(val.Max, stat.Value)
							val.Min = math.Min(val.Min, stat.Value)
						} else {
							secondaryScoreMap[stat.Type] = &SecondaryScore{stat.Type, stat.Value, stat.Value}
						}
					}

					secondaryStats = append(secondaryStats, &SecondaryStat{stat, 0})
				})

				mod := Mod{
					modUid,
					slot,
					set,
					level,
					pips,
					0,
					character,
					PrimaryStat{primaryStat},
					secondaryStats,
				}

				modChan <- &mod
			})
		}(i)
	}

	go func() {
		wg.Wait()
		close(modChan)
	}()

	for m := range modChan {
		mods = append(mods, m)
	}

	for _, m := range mods {
		totalScore := 0
		for _, s := range m.SecondaryStats {
			score := math.Max(0, (s.Value-secondaryScoreMap[s.Type].Min)/(secondaryScoreMap[s.Type].Max-secondaryScoreMap[s.Type].Min)*100)
			s.Score = round(score)
			totalScore += s.Score
		}
		m.TotalScore = totalScore
	}

	sort.Slice(mods, func(i, j int) bool {
		return mods[i].TotalScore > mods[j].TotalScore
	})

	for _, m := range mods {
		log.Printf("Score: %f, Uid: %v, Slot: %v, Type: %v, Pips: %v, Level: %v, Character: %v, Pri Type: %v, Pri Value: %v", m.TotalScore, m.Uid, m.Slot, m.Set, m.Pips, m.Level, m.CharacterName, m.PrimaryStat.Type, m.PrimaryStat.Value)
	}

	return mods
}

func favicon(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

type ModData struct {
	Mods []*Mod
}

func main() {
	tmpl := template.Must(template.ParseFiles("static/index.html"))

	fs := http.FileServer(http.Dir("static/resources"))
	http.Handle("/resources/", http.StripPrefix("/resources/", fs))

	http.HandleFunc("/favicon.ico", favicon)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Serving %s", r.URL.String())
		user := r.URL.Query().Get("u")

		if user != "" {
			mods := getMods(user)
			tmpl.Execute(w, ModData{Mods: mods})
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	log.Printf("Starting Mod Manager on port %d", *httpPort)

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *httpPort), nil))
}
