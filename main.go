/*
 * Copyright 2015 Édouard Hue
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"bufio"
	"bytes"
	"cgt.name/pkg/go-mwclient"
	"cgt.name/pkg/go-mwclient/params"
	"encoding/csv"
	"flag"
	"github.com/jmcvetta/napping"
	"io"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

/*
 * Some nearly-constants.
 */

const APP_VERSION = "ProsperoBot/Collections/1.0"

const COMMONS_API_URL = "https://commons.wikimedia.org/w/api.php"

const WDQ_API_URL = "https://wdq.wmflabs.org/api"

const COMMONS_CAT_PROPERTY = "373"

const CATEGORY_NAMESPACE = "Category:"

/*
 * Target data structure. Will be passed to the page template.
 */

type specimen struct {
	OriginalName        string // Scientific name from catalog column #0 (used when there is no Wikidata item)
	VernacularName      string // Folk name from catalog column #1
	WikidataItemId      string // From catalog column #7; item's nature should be taxon
	CommonsCategoryName string // Retrieved from Wikidata item
	FileCount           int    // Retrieved from Commons
	SubCats             int    // Retrieved from Commons
	SubCatsFileCounts   int    // Retrieved from Commons
	TotalFiles          int    // Retrieved from Commons
	Treatment           string // From catalog columns #2 and #3
	AccessionNumber     string // From catalog column #6
	SpecimenCategory    string // From catalog column #8
}

/*
 * Structures for Wikidata Query interaction
 */

type wdqStatus struct {
	Error       string `json:"error"`
	Items       int    `json:"items"`
	QueryTime   string `json:"querytime"`
	ParsedQuery string `json:"parsed_query"`
}

type wdqResult struct {
	Status wdqStatus                  `json:"status"`
	Items  []int                      `json:"items"`
	Props  map[string][][]interface{} `json:"props"`
}

type wdqQuery struct {
	q     string
	props string
}

/*
 * Intermediary structure for Commons interaction
 */

type categoryInfo struct {
	files        int
	subCats      int
	subCatsFiles int
}

/*
 * Command line arguments
 */

var csvLocation string
var templateLocation string
var wikiApiUrl string
var pageTitle string
var sectionNumber string

var commons, wiki *mwclient.Client

func check(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

func initFlags() *flag.FlagSet {
	set := flag.NewFlagSet("collections", flag.ExitOnError)
	set.StringVar(&csvLocation, "f", "", "CSV file location.")
	set.StringVar(&templateLocation, "t", "", "Template file location.")
	set.StringVar(&wikiApiUrl, "w", "", "Wiki API URL.")
	set.StringVar(&pageTitle, "p", "", "Page title.")
	set.StringVar(&sectionNumber, "s", "", "Section number.")
	
	return set
}

func main() {
	flags := initFlags()
	check(flags.Parse(os.Args[1:]))

	// Anonymous connection to Commons
	var commonsErr error
	commons, commonsErr = mwclient.New(COMMONS_API_URL, APP_VERSION)
	check(commonsErr)

	// Authenticated connection to the target wiki
	var wikiErr error
	wiki, wikiErr = mwclient.New(wikiApiUrl, APP_VERSION)
	check(wikiErr)

	login := os.Getenv("BOT_LOGIN")
	password := os.Getenv("BOT_PASSWORD")

	log.Printf("Will connect to %s with account %s\n", wikiApiUrl, login)

	err := wiki.Login(login, password)
	check(err)

	// Open specimens channel
	specimens := make(chan specimen)

	// Read specimens
	go readCsvFile(specimens)

	// Receive specimens
	c := make(chan int)
	go queryWdq(specimens, c)

	// Wait
	<-c

	log.Println("All done, logging out")

	// Say goodbye
	wiki.Logout()
}

// Read main CSV file and build incomplete items from it
func readCsvFile(specimens chan specimen) {
	
	log.Printf("Opening catalog file %s\n", csvLocation)
	
	f, err := os.Open(csvLocation)
	check(err)
	defer f.Close()
	defer close(specimens)

	br := bufio.NewReader(f)

	r := csv.NewReader(br)
	r.FieldsPerRecord = 9

	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		check(err)

		specimen := specimen{
			OriginalName:     record[0],
			VernacularName:   record[1],
			Treatment:        record[2] + " / " + record[3],
			AccessionNumber:  record[6],
			WikidataItemId:   record[7],
			SpecimenCategory: record[8],
		}

		specimens <- specimen
	}
	
	log.Println("Done reading catalog")
}

/*
 * Buffer all item IDs,
 *  then query WDQ for Category names (P373),
 *  then query Commons for file counts,
 *  then merge information into items,
 *  then generate target wiki page and publish it.
 */
func queryWdq(specimens chan specimen, c chan int) {
	// We will need to retrieve items from their Wikidata item id.
	// We might have several items for the same Wikidata item.
	specimensByWikidataId := make(map[string][]specimen)
	// We will also need to retrieve categories by their name
	categoryInfo := make(map[string]categoryInfo)

	// Join all Wikidata item ids and build a lookup map in the same loop
	var wikidataIds []string
	for i := range specimens {
		wikidataId := strings.TrimPrefix(i.WikidataItemId, "Q")
		wikidataIds = append(wikidataIds, wikidataId)
		specimensByWikidataId[wikidataId] = append(specimensByWikidataId[wikidataId], i)
	}

	query := napping.Params{
		"q":     "ITEMS[" + strings.Join(wikidataIds, ",") + "]",
		"props": COMMONS_CAT_PROPERTY,
	}
	result := wdqResult{}

	// Query WDQ
	log.Printf("Querying WDQ with params %s\n", &query)
	resp, err := napping.Get(WDQ_API_URL, &query, &result, nil)
	check(err)

	if resp.Status() == 200 {
		if result.Status.Error != "OK" {
			panic(result.Status.Error)
		}

		log.Println("Handling answer from WDQ")

		// We build an array of all Commons category names, then slice it up in multiple Commons queries.
		var categoryNames []string
		// For Commons queries synchronisation
		var commonsQueries []chan int

		// Position in categoryNames
		cursor := 0
		// Loop on WDQ result
		for i, itemProp := range result.Props[COMMONS_CAT_PROPERTY] {
			itemId := int(itemProp[0].(float64))
			categoryName := itemProp[2].(string)

			// Set specimens Commons category name
			itemSpecimens := specimensByWikidataId[strconv.Itoa(itemId)]
			for i, specimen := range itemSpecimens {
				specimen.CommonsCategoryName = CATEGORY_NAMESPACE + categoryName
				itemSpecimens[i] = specimen
			}

			// Accumulate Commons category names and query when reaching API limit
			categoryNames = append(categoryNames, CATEGORY_NAMESPACE+categoryName)
			if i > 0 && i%50 == 0 {
				cursor = i
				q := make(chan int)
				commonsQueries = append(commonsQueries, q)
				go queryCommons(categoryInfo, categoryNames[i-50:i], q)
			}
		}
		// Make the last query with remaining categories
		q := make(chan int)
		commonsQueries = append(commonsQueries, q)
		go queryCommons(categoryInfo, categoryNames[cursor:], q)

		// Wait for queries to terminate
		for _, q := range commonsQueries {
			<-q
		}
	} else {
		panic(resp.Status)
	}

	// Now we can update specimens with Commons information
	updatedSpecimens := make(chan specimen)
	defer close(updatedSpecimens)

	// Start page update routine
	go updateWikiPage(updatedSpecimens, c)

	for _, itemSpecimens := range specimensByWikidataId {
		for _, specimen := range itemSpecimens {
			// Lookup category information
			thisCategoryInfo := categoryInfo[specimen.CommonsCategoryName]

			// Update specimen
			specimen.FileCount = thisCategoryInfo.files
			specimen.SubCats = thisCategoryInfo.subCats
			specimen.SubCatsFileCounts = thisCategoryInfo.subCatsFiles
			specimen.TotalFiles = specimen.FileCount + specimen.SubCatsFileCounts

			// Send it to page update routine
			updatedSpecimens <- specimen
		}
	}

}

/*
 * Query Commons for categoryinfo about a bunch of categories.
 * If a category has subcategories, also query for each subcat's categoryinfo
 */
func queryCommons(categoryMembers map[string]categoryInfo, categoryNames []string, c chan int) {
	defer close(c)

	log.Printf("Querying Commons for %d categories\n", len(categoryNames))

	parameters := params.Values{
		"action":        "query",
		"prop":          "categoryinfo",
		"titles":        strings.Join(categoryNames, "|"),
		"continue":      "",
		"formatversion": "2",
	}

	q := commons.NewQuery(parameters)
	for q.Next() {
		resp := q.Resp()
		pages, err := resp.GetObjectArray("query", "pages")
		check(err)
		for _, page := range pages {
			title, err := page.GetString("title")
			check(err)

			thisCategoryInfo := categoryMembers[title]

			files, err := page.GetInt64("categoryinfo", "files")
			if err == nil {
				thisCategoryInfo.files = int(files)
			} else {
				thisCategoryInfo.files = 0
			}
			subcats, err := page.GetInt64("categoryinfo", "subcats")
			if err == nil {
				thisCategoryInfo.subCats = int(subcats)
			} else {
				thisCategoryInfo.subCats = 0
			}

			if thisCategoryInfo.subCats > 0 {
				thisCategoryInfo.subCatsFiles = queryCommonsSubcats(title)
			}

			categoryMembers[title] = thisCategoryInfo
		}
	}
}

/*
 * Count files in one's category subcategories.
 */
func queryCommonsSubcats(categoryName string) int {
	
	log.Printf("Querying Commons for subcategories of %s\n", categoryName)

	parameters := params.Values{
		"action":        "query",
		"prop":          "categoryinfo",
		"generator":     "categorymembers",
		"gcmtitle":      categoryName,
		"gcmtype":       "subcat",
		"continue":      "",
		"formatversion": "2",
	}

	totalFiles := 0

	q := commons.NewQuery(parameters)

	for q.Next() {
		resp := q.Resp()
		pages, err := resp.GetObjectArray("query", "pages")
		check(err)
		for _, page := range pages {
			files, err := page.GetInt64("categoryinfo", "files")
			if err == nil {
				totalFiles += int(files)
			}
		}
	}

	return totalFiles
}

type byOriginalName []specimen

func (a byOriginalName) Len() int { return len(a) }
func (a byOriginalName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byOriginalName) Less(i, j int) bool { return a[i].OriginalName < a[j].OriginalName }

/*
 * Generate a page section by merging the specimens in the provided template.
 */
func updateWikiPage(specimens chan specimen, c chan int) {
	defer close(c)

	var specimensBuffer []specimen
	for specimen := range specimens {
		specimensBuffer = append(specimensBuffer, specimen)
	}
	sort.Sort(byOriginalName(specimensBuffer))	

	log.Printf("About to update page %s", pageTitle)

	templateBytes, err := ioutil.ReadFile(templateLocation)
	check(err)

	tableTemplate, err := template.New("table").Delims("¤{", "}¤").Parse(string(templateBytes))
	check(err)

	var buf bytes.Buffer

	tableTemplate.Execute(&buf, specimensBuffer)

	parameters := params.Values{
		"title":    pageTitle,
		"section":  sectionNumber,
		"text":     buf.String(),
		"summary":  "Mise à jour",
		"notminor": "",
	}

	e := wiki.Edit(parameters)
	check(e)
}
