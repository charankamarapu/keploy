// Package replay provides functions for replaying requests and comparing responses.
package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"bytes"
	"os"

	"go.uber.org/zap"

	"github.com/fatih/color"
	"github.com/k0kubun/pp/v3"
	"github.com/olekukonko/tablewriter"
	"github.com/tidwall/gjson"
	"github.com/wI2L/jsondiff"
	"go.keploy.io/server/v2/pkg"
	"go.keploy.io/server/v2/pkg/models"
	"go.keploy.io/server/v2/utils"
)

type ValidatedJSON struct {
	expected    interface{} // The expected JSON
	actual      interface{} // The actual JSON
	isIdentical bool
}

type JSONComparisonResult struct {
	matches     bool     // Indicates if the JSON strings match according to the criteria
	isExact     bool     // Indicates if the match is exact, considering ordering and noise
	differences []string // Lists the keys or indices of values that are not the same
}

func match(tc *models.TestCase, actualResponse *models.HTTPResp, noiseConfig map[string]map[string][]string, ignoreOrdering bool, logger *zap.Logger) (bool, *models.Result) {
	bodyType := models.BodyTypePlain
	if json.Valid([]byte(actualResponse.Body)) {
		bodyType = models.BodyTypeJSON
	}
	pass := true
	hRes := &[]models.HeaderResult{}

	res := &models.Result{
		StatusCode: models.IntResult{
			Normal:   false,
			Expected: tc.HTTPResp.StatusCode,
			Actual:   actualResponse.StatusCode,
		},
		BodyResult: []models.BodyResult{{
			Normal:   false,
			Type:     bodyType,
			Expected: tc.HTTPResp.Body,
			Actual:   actualResponse.Body,
		}},
	}
	noise := tc.Noise

	var (
		bodyNoise   = noiseConfig["body"]
		headerNoise = noiseConfig["header"]
	)

	if bodyNoise == nil {
		bodyNoise = map[string][]string{}
	}
	if headerNoise == nil {
		headerNoise = map[string][]string{}
	}

	for field, regexArr := range noise {
		a := strings.Split(field, ".")
		if len(a) > 1 && a[0] == "body" {
			x := strings.Join(a[1:], ".")
			bodyNoise[x] = regexArr
		} else if a[0] == "header" {
			headerNoise[a[len(a)-1]] = regexArr
		}
	}

	// stores the json body after removing the noise
	cleanExp, cleanAct := tc.HTTPResp.Body, actualResponse.Body
	var jsonComparisonResult JSONComparisonResult
	if !Contains(MapToArray(noise), "body") && bodyType == models.BodyTypeJSON {
		//validate the stored json
		validatedJSON, err := ValidateAndMarshalJSON(logger, &cleanExp, &cleanAct)
		if err != nil {
			return false, res
		}
		if validatedJSON.isIdentical {
			jsonComparisonResult, err = JSONDiffWithNoiseControl(validatedJSON, bodyNoise, ignoreOrdering)
			pass = jsonComparisonResult.isExact
			if err != nil {
				return false, res
			}
		} else {
			pass = false
		}

		// debug log for cleanExp and cleanAct
		logger.Debug("cleanExp", zap.Any("", cleanExp))
		logger.Debug("cleanAct", zap.Any("", cleanAct))
	} else {
		if !Contains(MapToArray(noise), "body") && tc.HTTPResp.Body != actualResponse.Body {
			pass = false
		}
	}

	res.BodyResult[0].Normal = pass

	if !CompareHeaders(pkg.ToHTTPHeader(tc.HTTPResp.Header), pkg.ToHTTPHeader(actualResponse.Header), hRes, headerNoise) {

		pass = false
	}

	res.HeadersResult = *hRes
	if tc.HTTPResp.StatusCode == actualResponse.StatusCode {
		res.StatusCode.Normal = true
	} else {

		pass = false
	}

	if !pass {
		logDiffs := NewDiffsPrinter(tc.Name)

		newLogger := pp.New()
		newLogger.WithLineInfo = false
		newLogger.SetColorScheme(models.FailingColorScheme)
		var logs = ""

		logs = logs + newLogger.Sprintf("Testrun failed for testcase with id: %s\n\n--------------------------------------------------------------------\n\n", tc.Name)

		// ------------ DIFFS RELATED CODE -----------
		if !res.StatusCode.Normal {
			logDiffs.PushStatusDiff(fmt.Sprint(res.StatusCode.Expected), fmt.Sprint(res.StatusCode.Actual))
		}

		var (
			actualHeader   = map[string][]string{}
			expectedHeader = map[string][]string{}
			unmatched      = true
		)

		for _, j := range res.HeadersResult {
			if !j.Normal {
				unmatched = false
				actualHeader[j.Actual.Key] = j.Actual.Value
				expectedHeader[j.Expected.Key] = j.Expected.Value
			}
		}

		if !unmatched {
			for i, j := range expectedHeader {
				logDiffs.PushHeaderDiff(fmt.Sprint(j), fmt.Sprint(actualHeader[i]), i, headerNoise)
			}
		}

		if !res.BodyResult[0].Normal {
			if json.Valid([]byte(actualResponse.Body)) {
				patch, err := jsondiff.Compare(tc.HTTPResp.Body, actualResponse.Body)
				if err != nil {
					logger.Warn("failed to compute json diff", zap.Error(err))
				}
				for _, op := range patch {
					if jsonComparisonResult.matches {
						logDiffs.hasarrayIndexMismatch = true
						logDiffs.PushFooterDiff(strings.Join(jsonComparisonResult.differences, ", "))
					}
					logDiffs.PushBodyDiff(fmt.Sprint(op.OldValue), fmt.Sprint(op.Value), bodyNoise)

				}
			} else {
				logDiffs.PushBodyDiff(fmt.Sprint(tc.HTTPResp.Body), fmt.Sprint(actualResponse.Body), bodyNoise)
			}
		}
		_, err := newLogger.Printf(logs)
		if err != nil {
			utils.LogError(logger, err, "failed to print the logs")
		}

		err = logDiffs.Render()
		if err != nil {
			utils.LogError(logger, err, "failed to render the diffs")
		}
	} else {
		newLogger := pp.New()
		newLogger.WithLineInfo = false
		newLogger.SetColorScheme(models.PassingColorScheme)
		var log2 = ""
		log2 += newLogger.Sprintf("Testrun passed for testcase with id: %s\n\n--------------------------------------------------------------------\n\n", tc.Name)
		_, err := newLogger.Printf(log2)
		if err != nil {
			utils.LogError(logger, err, "failed to print the logs")
		}
	}
	return pass, res
}

func FlattenHTTPResponse(h http.Header, body string) (map[string][]string, error) {
	m := map[string][]string{}
	for k, v := range h {
		m["header."+k] = []string{strings.Join(v, "")}
	}
	err := AddHTTPBodyToMap(body, m)
	if err != nil {
		return m, err
	}
	return m, nil
}

// UnmarshallJSON returns unmarshalled JSON object.
func UnmarshallJSON(s string, log *zap.Logger) (interface{}, error) {
	var result interface{}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		utils.LogError(log, err, "cannot convert json string into json object")
		return nil, err
	}
	return result, nil
}

func ArrayToMap(arr []string) map[string]bool {
	res := map[string]bool{}
	for i := range arr {
		res[arr[i]] = true
	}
	return res
}

func InterfaceToString(val interface{}) string {
	switch v := val.(type) {
	case int:
		return fmt.Sprintf("%d", v)
	case float64:
		return fmt.Sprintf("%f", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func JSONDiffWithNoiseControl(validatedJSON ValidatedJSON, noise map[string][]string, ignoreOrdering bool) (JSONComparisonResult, error) {
	var matchJSONComparisonResult JSONComparisonResult
	matchJSONComparisonResult, err := matchJSONWithNoiseHandling("", validatedJSON.expected, validatedJSON.actual, noise, ignoreOrdering)
	if err != nil {
		return matchJSONComparisonResult, err
	}

	return matchJSONComparisonResult, nil
}

func ValidateAndMarshalJSON(log *zap.Logger, exp, act *string) (ValidatedJSON, error) {
	var validatedJSON ValidatedJSON
	expected, err := UnmarshallJSON(*exp, log)
	if err != nil {
		return validatedJSON, err
	}
	actual, err := UnmarshallJSON(*act, log)
	if err != nil {
		return validatedJSON, err
	}
	validatedJSON.expected = expected
	validatedJSON.actual = actual
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		validatedJSON.isIdentical = false
		return validatedJSON, nil
	}
	cleanExp, err := json.Marshal(expected)
	if err != nil {
		return validatedJSON, err
	}
	cleanAct, err := json.Marshal(actual)
	if err != nil {
		return validatedJSON, err
	}
	*exp = string(cleanExp)
	*act = string(cleanAct)
	validatedJSON.isIdentical = true
	return validatedJSON, nil
}

// matchJSONWithNoiseHandling returns strcut if expected and actual JSON objects matches(are equal) and in exact order(isExact).
func matchJSONWithNoiseHandling(key string, expected, actual interface{}, noiseMap map[string][]string, ignoreOrdering bool) (JSONComparisonResult, error) {
	var matchJSONComparisonResult JSONComparisonResult
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		return matchJSONComparisonResult, errors.New("type not matched")
	}
	if expected == nil && actual == nil {
		matchJSONComparisonResult.isExact = true
		matchJSONComparisonResult.matches = true
		return matchJSONComparisonResult, nil
	}
	x := reflect.ValueOf(expected)
	prefix := ""
	if key != "" {
		prefix = key + "."
	}
	switch x.Kind() {
	case reflect.Float64, reflect.String, reflect.Bool:
		regexArr, isNoisy := CheckStringExist(key, noiseMap)
		if isNoisy && len(regexArr) != 0 {
			isNoisy, _ = MatchesAnyRegex(InterfaceToString(expected), regexArr)
		}
		if expected != actual && !isNoisy {
			return matchJSONComparisonResult, nil
		}

	case reflect.Map:
		expMap := expected.(map[string]interface{})
		actMap := actual.(map[string]interface{})
		copiedExpMap := make(map[string]interface{})
		copiedActMap := make(map[string]interface{})

		// Copy each key-value pair from expMap to copiedExpMap
		for key, value := range expMap {
			copiedExpMap[key] = value
		}

		// Repeat the same process for actual map
		for key, value := range actMap {
			copiedActMap[key] = value
		}
		isExact := true
		differences := []string{}
		for k, v := range expMap {
			val, ok := actMap[k]
			if !ok {
				return matchJSONComparisonResult, nil
			}
			if valueMatchJSONComparisonResult, er := matchJSONWithNoiseHandling(prefix+k, v, val, noiseMap, ignoreOrdering); !valueMatchJSONComparisonResult.matches || er != nil {
				return valueMatchJSONComparisonResult, nil
			} else if !valueMatchJSONComparisonResult.isExact {
				isExact = false
				differences = append(differences, k)
				differences = append(differences, valueMatchJSONComparisonResult.differences...)
			}
			// remove the noisy key from both expected and actual JSON.
			if _, ok := CheckStringExist(prefix+k, noiseMap); ok {
				delete(copiedExpMap, prefix+k)
				delete(copiedActMap, k)
				continue
			}
		}
		// checks if there is a key which is not present in expMap but present in actMap.
		for k := range actMap {
			_, ok := expMap[k]
			if !ok {
				return matchJSONComparisonResult, nil
			}
		}
		matchJSONComparisonResult.matches = true
		matchJSONComparisonResult.isExact = isExact
		matchJSONComparisonResult.differences = append(matchJSONComparisonResult.differences, differences...)
		return matchJSONComparisonResult, nil
	case reflect.Slice:
		if regexArr, isNoisy := CheckStringExist(key, noiseMap); isNoisy && len(regexArr) != 0 {
			break
		}
		expSlice := reflect.ValueOf(expected)
		actSlice := reflect.ValueOf(actual)
		if expSlice.Len() != actSlice.Len() {
			return matchJSONComparisonResult, nil
		}
		isMatched := true
		isExact := true
		for i := 0; i < expSlice.Len(); i++ {
			matched := false
			for j := 0; j < actSlice.Len(); j++ {
				if valMatchJSONComparisonResult, err := matchJSONWithNoiseHandling(key, expSlice.Index(i).Interface(), actSlice.Index(j).Interface(), noiseMap, ignoreOrdering); err == nil && valMatchJSONComparisonResult.matches {
					if !valMatchJSONComparisonResult.isExact {
						for _, val := range valMatchJSONComparisonResult.differences {
							prefixedVal := key + "[" + fmt.Sprint(j) + "]." + val // Prefix the value
							matchJSONComparisonResult.differences = append(matchJSONComparisonResult.differences, prefixedVal)
						}
					}
					matched = true
					break
				}
			}

			if !matched {
				isMatched = false
				isExact = false
				break
			}
		}
		if !isMatched {
			matchJSONComparisonResult.matches = isMatched
			matchJSONComparisonResult.isExact = isExact
			return matchJSONComparisonResult, nil
		}
		if !ignoreOrdering {
			for i := 0; i < expSlice.Len(); i++ {
				if valMatchJSONComparisonResult, er := matchJSONWithNoiseHandling(key, expSlice.Index(i).Interface(), actSlice.Index(i).Interface(), noiseMap, ignoreOrdering); er != nil || !valMatchJSONComparisonResult.isExact {
					isExact = false
					break
				}
			}
		}
		matchJSONComparisonResult.matches = isMatched
		matchJSONComparisonResult.isExact = isExact

		return matchJSONComparisonResult, nil
	default:
		return matchJSONComparisonResult, errors.New("type not registered for json")
	}
	matchJSONComparisonResult.matches = true
	matchJSONComparisonResult.isExact = true
	return matchJSONComparisonResult, nil
}

// MAX_LINE_LENGTH is chars PER expected/actual string. Can be changed no problem
const MAX_LINE_LENGTH = 50

type DiffsPrinter struct {
	testCase              string
	statusExp             string
	statusAct             string
	headerExp             map[string]string
	headerAct             map[string]string
	bodyExp               string
	bodyAct               string
	bodyNoise             map[string][]string
	headNoise             map[string][]string
	hasarrayIndexMismatch bool
	text                  string
}

func NewDiffsPrinter(testCase string) DiffsPrinter {
	return DiffsPrinter{testCase, "", "", map[string]string{}, map[string]string{}, "", "", map[string][]string{}, map[string][]string{}, false, ""}
}

func (d *DiffsPrinter) PushStatusDiff(exp, act string) {
	d.statusExp, d.statusAct = exp, act
}

func (d *DiffsPrinter) PushFooterDiff(key string) {
	d.hasarrayIndexMismatch = true
	d.text = key
}

func (d *DiffsPrinter) PushHeaderDiff(exp, act, key string, noise map[string][]string) {
	d.headerExp[key], d.headerAct[key], d.headNoise = exp, act, noise
}

func (d *DiffsPrinter) PushBodyDiff(exp, act string, noise map[string][]string) {
	d.bodyExp, d.bodyAct, d.bodyNoise = exp, act, noise
}

// Render will display and colorize diffs side-by-side
func (d *DiffsPrinter) Render() error {
	diffs := []string{}

	if d.statusExp != d.statusAct {
		diffs = append(diffs, sprintDiff(d.statusExp, d.statusAct, "status"))
	}

	diffs = append(diffs, sprintDiffHeader(d.headerExp, d.headerAct))

	if len(d.bodyExp) != 0 || len(d.bodyAct) != 0 {
		bE, bA := []byte(d.bodyExp), []byte(d.bodyAct)
		if json.Valid(bE) && json.Valid(bA) {
			difference, err := sprintJSONDiff(bE, bA, "body", d.bodyNoise)
			if err != nil {
				difference = sprintDiff(d.bodyExp, d.bodyAct, "body")
			}
			diffs = append(diffs, difference)
		} else {
			diffs = append(diffs, sprintDiff(d.bodyExp, d.bodyAct, "body"))
		}

	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetAutoWrapText(false)
	table.SetHeader([]string{fmt.Sprintf("Diffs %v", d.testCase)})
	table.SetHeaderColor(tablewriter.Colors{tablewriter.FgHiRedColor})
	table.SetAlignment(tablewriter.ALIGN_CENTER)

	for _, e := range diffs {
		table.Append([]string{e})
	}
	if d.hasarrayIndexMismatch {
		yellowPaint := color.New(color.FgYellow).SprintFunc()
		redPaint := color.New(color.FgRed).SprintFunc()
		startPart := " Expected and actual value"
		var midPartpaint string
		if len(d.text) > 0 {
			midPartpaint = redPaint(d.text)
			startPart += " of "
		}
		initalPart := yellowPaint(utils.WarningSign + startPart)

		endPaint := yellowPaint(" are in different order but have the same objects")
		table.SetHeader([]string{initalPart + midPartpaint + endPaint})
		table.SetAlignment(tablewriter.ALIGN_CENTER)
		table.Append([]string{initalPart + midPartpaint + endPaint})
	}
	table.Render()
	return nil
}

/*
 * Returns a nice diff table where the left is the expect and the right
 * is the actual. each entry in expect and actual will contain the key
 * and the corresponding value.
 */
func sprintDiffHeader(expect, actual map[string]string) string {

	expectAll := ""
	actualAll := ""
	for key, expValue := range expect {
		actValue := key + ": " + actual[key]
		expValue = key + ": " + expValue
		// Offset will be where the string start to unmatch
		offsets, _ := diffIndexRange(expValue, actValue)

		// Color of the unmatch, can be changed
		cE, cA := color.FgHiRed, color.FgHiGreen

		expectAll += breakWithColor(expValue, &cE, offsets)

		actualAll += breakWithColor(actValue, &cA, offsets)

	}
	if len(expect) > MAX_LINE_LENGTH || len(actual) > MAX_LINE_LENGTH {
		return expectActualTable(expectAll, actualAll, "header", false) // Don't centerize
	}
	return expectActualTable(expectAll, actualAll, "header", true)
}

/*
 * Returns a nice diff table where the left is the expect and the right
 * is the actual. For JSON-based diffs use SprintJSONDiff
 * field: body, status...
 */
func sprintDiff(expect, actual, field string) string {

	// Offset will be where the string start to unmatch
	offset, _ := diffIndexRange(expect, actual)
	// Color of the unmatch, can be changed
	cE, cA := color.FgHiRed, color.FgHiGreen

	var exp, act string

	exp += breakWithColor(expect, &cE, offset)

	act += breakWithColor(actual, &cA, offset)

	// act := breakWithColor(actual, &cA, offset)
	if len(expect) > MAX_LINE_LENGTH || len(actual) > MAX_LINE_LENGTH {
		return expectActualTable(exp, act, field, false) // Don't centerize
	}
	return expectActualTable(exp, act, field, true)
}

/* This will return the json diffs in a beautifull way. It will in fact
 * create a colorized table-based expect-response string and return it.
 * on the left-side there'll be the expect and on the right the actual
 * response. Its important to mention the inputs must to be a json. If
 * the body isnt in the rest-api formats (what means it is not json-based)
 * its better to use a generic diff output as the SprintDiff.
 */
func sprintJSONDiff(json1 []byte, json2 []byte, field string, noise map[string][]string) (string, error) {
	diffString, err := calculateJSONDiffs(json1, json2)
	if err != nil {
		return "", err
	}
	expect, actual := separateAndColorize(diffString, noise)
	result := expectActualTable(expect, actual, field, false)
	return result, nil
}

// Find the diff between two strings returning index where
// the difference begin
func diffIndexRange(s1, s2 string) ([]Range, bool) {
	var ranges []Range
	diff := false

	maxLen := len(s1)
	if len(s2) > maxLen {
		maxLen = len(s2)
	}

	var startDiff = -1
	for i := 0; i < maxLen; i++ {
		char1, char2 := byte(0), byte(0)
		if i < len(s1) {
			char1 = s1[i]
		}
		if i < len(s2) {
			char2 = s2[i]
		}

		if char1 != char2 {
			if startDiff == -1 {
				startDiff = i
			}
			diff = true
		} else {
			if startDiff != -1 {
				ranges = append(ranges, Range{Start: startDiff, End: i - 1})
				startDiff = -1
			}
		}
	}

	if startDiff != -1 {
		ranges = append(ranges, Range{Start: startDiff, End: maxLen - 1})
	}

	return ranges, diff
}

/* Will perform the calculation of the diffs, returning a string that
 * containes the lines that does not match represented by either a
 * minus or add symbol followed by the respective line.
 */
func calculateJSONDiffs(json1 []byte, json2 []byte) (string, error) {
	result1 := gjson.ParseBytes(json1)
	result2 := gjson.ParseBytes(json2)

	var diffStrings []string
	result1.ForEach(func(key, value gjson.Result) bool {
		value2 := result2.Get(key.String())
		if !value2.Exists() {
			diffStrings = append(diffStrings, fmt.Sprintf("- \"%s\": %v", key, value))
		} else if value.String() != value2.String() {
			diffStrings = append(diffStrings, fmt.Sprintf("- \"%s\": %v", key, value))
			diffStrings = append(diffStrings, fmt.Sprintf("+ \"%s\": %v", key, value2))
		}
		return true
	})

	result2.ForEach(func(key, value gjson.Result) bool {
		if !result1.Get(key.String()).Exists() {
			diffStrings = append(diffStrings, fmt.Sprintf("+ \"%s\": %v", key, value))
		}
		return true
	})

	return strings.Join(diffStrings, "\n"), nil
}
func writeKeyValuePair(sb *strings.Builder, key string, value interface{}, isDifferent bool, indent string, colorize func(a ...interface{}) string) {
	var valStr string
	switch reflect.TypeOf(value).Kind() {
	case reflect.Slice, reflect.Map:
		marshaledValue, err := json.Marshal(value)
		if err != nil {
			valStr = "error"
		} else {
			valStr = string(marshaledValue)
		}
	default:
		valStr = fmt.Sprint(value)
	}

	if isDifferent {
		valStr = colorize(valStr)
	}

	sb.WriteString(fmt.Sprintf("%s\"%s\": %s,\n", indent, key, valStr))
}

// compareAndColorizeSlices compares two slices of interfaces and outputs color-coded differences.
// compareAndColorizeSlices compares two slices of interfaces and outputs color-coded differences.
func compareAndColorizeSlices(a, b []interface{}, indent string, red, green func(a ...interface{}) string) (string, string) {
	var expectedOutput strings.Builder
	var actualOutput strings.Builder

	maxLength := len(a)
	if len(b) > maxLength {
		maxLength = len(b)
	}
	reds := color.New(color.FgRed).SprintFunc()
	greens := color.New(color.FgGreen).SprintFunc()
	for i := 0; i < maxLength; i++ {
		var aValue, bValue interface{}
		var aExists, bExists bool

		if i < len(a) {
			aValue = a[i]
			aExists = true
		}

		if i < len(b) {
			bValue = b[i]
			bExists = true
		}

		if aExists && bExists {
			// Both slices have this index, compare values
			expectedText, actualText := compareValues(aValue, bValue, indent+"  ", reds, greens)
			if expectedText != "" {
				expectedOutput.WriteString(fmt.Sprintf("%s[%d]: %s\n", indent, i, expectedText))
			}
			if actualText != "" {
				actualOutput.WriteString(fmt.Sprintf("%s[%d]: %s\n", indent, i, actualText))
			}
		} else if aExists {
			// Only a has this index, it's a deletion
			expectedOutput.WriteString(fmt.Sprintf("%s[%d]: %s\n", indent, i, red("%v", aValue)))
		} else if bExists {
			// Only b has this index, it's an addition
			actualOutput.WriteString(fmt.Sprintf("%s[%d]: %s\n", indent, i, green("%v", bValue)))
		}
	}

	return expectedOutput.String(), actualOutput.String()
}

// compare compares the values and decides whether to call compareAndColorizeMap or compareAndColorizeSlices.
func compare(key string, val1, val2 interface{}, indent string, expect, actual *strings.Builder, red, green func(a ...interface{}) string) {
	switch v1 := val1.(type) {
	case string:
		if v2, ok := val2.(string); ok {
			// When both values are strings, highlight word differences
			diff1, diff2 := highlightDifferences(v1, v2, red, green)
			writeKeyValuePair(expect, key, diff1, true, indent, red)
			writeKeyValuePair(actual, key, diff2, true, indent, green)
		}
	default:
		// Fallback to simple comparison for non-string types or if types differ
		writeKeyValuePair(expect, key, val1, true, indent, red)
		writeKeyValuePair(actual, key, val2, true, indent, green)
	}
}

// highlightDifferences will compare words and wrap differences with ANSI color codes.
func highlightDifferences(str1, str2 string, red, green func(a ...interface{}) string) (string, string) {
	words1 := strings.Fields(str1)
	words2 := strings.Fields(str2)
	var diff1, diff2 []string

	maxLen := len(words1)
	if len(words2) > maxLen {
		maxLen = len(words2)
	}

	for i := 0; i < maxLen; i++ {
		word1 := ""
		word2 := ""
		if i < len(words1) {
			word1 = words1[i]
		}
		if i < len(words2) {
			word2 = words2[i]
		}

		if word1 != word2 {
			diff1 = append(diff1, red(word1))
			diff2 = append(diff2, green(word2))
		} else {
			diff1 = append(diff1, word1)
			diff2 = append(diff2, word2)
		}
	}

	return strings.Join(diff1, " "), strings.Join(diff2, " ")
}

type JSONMap map[string]interface{}

func compareAndColorizeMaps(a, b JSONMap, indent string) (string, string) {
	var expectedOutput strings.Builder
	var actualOutput strings.Builder
	red := color.New(color.FgRed).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()

	// Serialize the value to JSON, handle errors properly in your production code.
	serialize := func(value interface{}) string {
		bytes, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			// Handle error in your actual code
			return "error"
		}
		return string(bytes)
	}

	// Find keys that are in a but not in b (deleted in b)
	for key, aValue := range a {
		bValue, exists := b[key]
		if !exists {
			// Key is missing in b, so it's a deletion
			expectedOutput.WriteString(red(indent + "- \"" + key + "\": " + serialize(aValue) + "\n"))
			continue
		}

		// If values are different, compare them
		if !reflect.DeepEqual(aValue, bValue) {
			expectedOutput.WriteString(indent + "\"" + key + "\": " + serialize(aValue) + "\n")
			actualOutput.WriteString(indent + "\"" + key + "\": " + serialize(bValue) + "\n")
		}
	}

	// Find keys that are in b but not in a (added in b)
	for key, bValue := range b {
		if _, exists := a[key]; !exists {
			// Key is new in b, so it's an addition
			actualOutput.WriteString(green(indent + "+ \"" + key + "\": " + serialize(bValue) + "\n"))
		}
	}

	return expectedOutput.String(), actualOutput.String()
}

// writeKeyValuePair writes a key-value pair with optional color.
// writeKeyValuePair writes a key-value pair with optional color.

// Will receive a string that has the differences represented
// by a plus or a minus sign and separate it. Just works with json
func separateAndColorize(diffStr string, noise map[string][]string) (string, string) {
	lines := strings.Split(diffStr, "\n")
	expects := make(map[string]interface{}, 0)
	actuals := make(map[string]interface{}, 0)
	s := true
	sd, ad := "", ""
	sds, ads := "", ""
	for i, line := range lines {
		if len(line) > 0 && line[0] == '-' && i != len(lines)-1 {

			// Remove the leading "- " or "+ "
			actualTrimmedLine := lines[i+1][3:]
			actualkeyValue := strings.SplitN(actualTrimmedLine, ":", 2)
			if len(actualkeyValue) == 2 {
				key := strings.TrimSpace(actualkeyValue[0])
				value := strings.TrimSpace(actualkeyValue[1])

				var jsonObj map[string]interface{}
				err := json.Unmarshal([]byte(value), &jsonObj)
				if err != nil {
					fmt.Println("Error parsing JSON:", err)
					s = false
				}

				expects = map[string]interface{}{key: jsonObj}
			}

			// Remove the leading "- " or "+ "
			expectedtrimmedLine := line[3:]
			expectedkeyValue := strings.SplitN(expectedtrimmedLine, ":", 2)
			if len(expectedkeyValue) == 2 {
				key := strings.TrimSpace(expectedkeyValue[0])
				value := strings.TrimSpace(expectedkeyValue[1])

				var jsonObj map[string]interface{}
				err := json.Unmarshal([]byte(value), &jsonObj)
				if err != nil {
					fmt.Println("Error parsing JSON:", err)
					s = false
				}

				actuals = map[string]interface{}{key: jsonObj}

			}
			// diffs := diffJSONObjects(expects, actuals)
			expectedText, actualText := compareAndColorizeMaps(expects, actuals, " ")
			// sds, ads = diffJSONObjects(expects, actuals)
			fmt.Println(expectedText, actualText)
			sd += breakLines(sds)
			ad += breakLines(ads)
			expects = make(map[string]interface{}, 0)
			actuals = make(map[string]interface{}, 0)
		}

	}
	if s {
		return sd, ad

	}
	expect, actual := "", ""
	diffLines := strings.Split(diffStr, "\n")
	for i, line := range diffLines {
		if len(line) > 0 {
			noised := false

			for e := range noise {
				// If contains noise remove diff flag
				if strings.Contains(line, e) {

					if line[0] == '-' {
						line = " " + line[1:]
						expect += breakWithColor(line, nil, []Range{})
					} else if line[0] == '+' {
						line = " " + line[1:]
						actual += breakWithColor(line, nil, []Range{})
					}
					noised = true
				}
			}

			if noised {
				continue
			}
			if line[0] == '-' {
				c := color.FgRed
				// Workaround to get the exact index where the diff begins
				if diffLines[i+1][0] == '+' {

					/* As we want to get the exact difference where the line's
					 * diff begin we must to, first, get the expect (this) and
					 * the actual (next) line. Then we must to espace the first
					 * char that is an "+" or "-" symbol so we end up having
					 * just the contents of the line we want to compare */
					offsets, _ := diffIndexRange(line[1:], diffLines[i+1][1:])
					expect += breakWithColor(line, &c, offsets)
				} else {
					// In the case where there isn't in fact an actual
					// version to compare, it was just expect to have this
					expect += breakWithColor(line, &c, []Range{})
				}
			} else if line[0] == '+' {
				c := color.FgGreen

				// Here we do the same thing as above, just inverted
				if diffLines[i-1][0] == '-' {
					offsets, _ := diffIndexRange(line[1:], diffLines[i-1][1:])
					actual += breakWithColor(line, &c, offsets)
				} else {
					actual += breakWithColor(line, &c, []Range{})
				}
			} else {
				expect += breakWithColor(line, nil, []Range{})
				actual += breakWithColor(line, nil, []Range{})
			}
		}
	}

	return expect, actual
}

// Will colorize the strubg and do the job of break it if it pass MAX_LINE_LENGTH,
// always respecting the reset of ascii colors before the break line to dont
func breakWithColor(input string, c *color.Attribute, highlightRanges []Range) string {
	paint := func(a ...interface{}) string { return "" }
	if c != nil {
		paint = color.New(*c).SprintFunc()
	}
	var output strings.Builder
	var isColorRange bool
	lineLen := 0

	for i, char := range input {
		isColorRange = false // Reset for each character

		// Determine if this character is within any of the color ranges
		for _, r := range highlightRanges {
			if i >= r.Start+1 && i < r.End+2 {
				isColorRange = true
				break
			}
		}

		if isColorRange {
			output.WriteString(paint(string(char)))
		} else {
			output.WriteString(string(char))
		}

		// Increment line length, wrap line if necessary
		lineLen++
		if lineLen == MAX_LINE_LENGTH {
			output.WriteString("\n")
			lineLen = 0 // Reset line length after a newline
		}
	}

	// Catch any case where the input does not end with a newline
	if lineLen > 0 {
		output.WriteString("\n")
	}

	return output.String()
}
func compareValues(a, b interface{}, indent string, red, green func(a ...interface{}) string) (string, string) {
	var expectedOutput strings.Builder
	var actualOutput strings.Builder

	if reflect.DeepEqual(a, b) {
		return "", ""
	}
	reds := color.New(color.FgRed).SprintFunc()
	greens := color.New(color.FgGreen).SprintFunc()
	switch aValue := a.(type) {
	case JSONMap:
		if bValue, ok := b.(JSONMap); ok {
			expectedText, actualText := compareAndColorizeMaps(aValue, bValue, indent)
			expectedOutput.WriteString(expectedText)
			actualOutput.WriteString(actualText)
		} else {
			expectedOutput.WriteString(red("%v", aValue))
			actualOutput.WriteString(green("%v", b))
		}
	case []interface{}:
		if bValue, ok := b.([]interface{}); ok {
			expectedText, actualText := compareAndColorizeSlices(aValue, bValue, indent, reds, greens)
			expectedOutput.WriteString(expectedText)
			actualOutput.WriteString(actualText)
		} else {
			expectedOutput.WriteString(red("%v", aValue))
			actualOutput.WriteString(green("%v", b))
		}
	case string:
		if bValue, ok := b.(string); ok {
			expectedOutput.WriteString(red(aValue))
			actualOutput.WriteString(green(bValue))
		} else {
			expectedOutput.WriteString(red("%v", aValue))
			actualOutput.WriteString(green("%v", b))
		}
	default:
		expectedOutput.WriteString(red("%v", aValue))
		actualOutput.WriteString(green("%v", b))
	}

	return expectedOutput.String(), actualOutput.String()
}

func highlightWords(str1, str2 string, red, green func(format string, a ...interface{}) string) (string, string) {
	words1 := strings.Fields(str1)
	words2 := strings.Fields(str2)
	var diff1, diff2 []string

	maxLen := len(words1)
	if len(words2) > maxLen {
		maxLen = len(words2)
	}

	for i := 0; i < maxLen; i++ {
		word1 := ""
		word2 := ""
		if i < len(words1) {
			word1 = words1[i]
		}
		if i < len(words2) {
			word2 = words2[i]
		}

		if word1 != word2 {
			diff1 = append(diff1, red(word1))
			diff2 = append(diff2, green(word2))
		} else {
			diff1 = append(diff1, word1)
			diff2 = append(diff2, word2)
		}
	}

	return strings.Join(diff1, " "), strings.Join(diff2, " ")
}

var ansiColorCodeRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleLength(str string) int {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return len(re.ReplaceAllString(str, ""))
}

const ansiStart = "\033[31m" // Example: red font
const ansiEnd = "\033[0m"    // Reset to default

// breakLines breaks long lines into a specified length and ensures ANSI codes are correctly placed.
func breakLines(input string) string {
	const MAX_LINE_LENGTH = 80 // Set your desired max line length
	var output strings.Builder
	var currentLine strings.Builder
	inANSISequence := false
	lineLength := 0

	// We'll collect ANSI sequences here and then reapply them as needed
	var ansiSequences []string
	var ansiSequenceBuilder strings.Builder

	for _, char := range input {
		// Check for the beginning of an ANSI sequence
		if char == '\x1b' {
			inANSISequence = true
			ansiSequenceBuilder.Reset()
			ansiSequenceBuilder.WriteRune(char)
			continue
		}
		if inANSISequence {
			// Continue collecting the ANSI sequence
			ansiSequenceBuilder.WriteRune(char)
			if char == 'm' {
				// End of the ANSI sequence
				inANSISequence = false
				ansiSequences = append(ansiSequences, ansiSequenceBuilder.String())
				continue
			}
		} else {
			currentLine.WriteRune(char)
			if !isControlCharacter(char) {
				lineLength++
			}
		}

		// Time to wrap the line
		if lineLength >= MAX_LINE_LENGTH && char == ' ' && !inANSISequence {
			lastSpace := strings.LastIndex(currentLine.String(), " ")
			if lastSpace == -1 {
				lastSpace = MAX_LINE_LENGTH
			}
			output.WriteString(currentLine.String()[:lastSpace])
			output.WriteString("\n")
			currentLine.Reset()
			lineLength = 0
			if len(ansiSequences) > 0 {
				// Reapply the last ANSI sequence if any
				output.WriteString(ansiSequences[len(ansiSequences)-1])
				currentLine.WriteString(ansiSequences[len(ansiSequences)-1])
			}
		}
	}

	// Write any remaining content
	output.WriteString(currentLine.String())
	return output.String()
}

func isControlCharacter(char rune) bool {
	return !unicode.IsPrint(char) && !unicode.IsSpace(char)
}

// Will return a string in a two columns table where the left
// side is the expected string and the right is the actual
// field: body, header, status...
func expectActualTable(exp string, act string, field string, centerize bool) string {
	buf := &bytes.Buffer{}
	table := tablewriter.NewWriter(buf)

	if centerize {
		table.SetAlignment(tablewriter.ALIGN_CENTER)
	}

	table.SetHeader([]string{fmt.Sprintf("Expect %v", field), fmt.Sprintf("Actual %v", field)})
	table.SetAutoWrapText(false)
	table.SetBorder(false)
	table.SetColMinWidth(0, MAX_LINE_LENGTH)
	table.SetColMinWidth(1, MAX_LINE_LENGTH)
	table.Append([]string{exp, act})
	table.Render()
	return buf.String()
}

func Contains(elems []string, v string) bool {
	for _, s := range elems {
		if v == s {
			return true
		}
	}
	return false
}

func checkKey(res *[]models.HeaderResult, key string) bool {
	for _, v := range *res {
		if key == v.Expected.Key {
			return false
		}
	}
	return true
}

func CompareHeaders(h1 http.Header, h2 http.Header, res *[]models.HeaderResult, noise map[string][]string) bool {
	if res == nil {
		return false
	}
	match := true
	_, isHeaderNoisy := noise["header"]
	for k, v := range h1 {
		regexArr, isNoisy := CheckStringExist(k, noise)
		if isNoisy && len(regexArr) != 0 {
			isNoisy, _ = MatchesAnyRegex(v[0], regexArr)
		}
		isNoisy = isNoisy || isHeaderNoisy
		val, ok := h2[k]
		if !isNoisy {
			if !ok {
				if checkKey(res, k) {
					*res = append(*res, models.HeaderResult{
						Normal: false,
						Expected: models.Header{
							Key:   k,
							Value: v,
						},
						Actual: models.Header{
							Key:   k,
							Value: nil,
						},
					})
				}

				match = false
				continue
			}
			if len(v) != len(val) {
				if checkKey(res, k) {
					*res = append(*res, models.HeaderResult{
						Normal: false,
						Expected: models.Header{
							Key:   k,
							Value: v,
						},
						Actual: models.Header{
							Key:   k,
							Value: val,
						},
					})
				}
				match = false
				continue
			}
			for i, e := range v {
				if val[i] != e {
					if checkKey(res, k) {
						*res = append(*res, models.HeaderResult{
							Normal: false,
							Expected: models.Header{
								Key:   k,
								Value: v,
							},
							Actual: models.Header{
								Key:   k,
								Value: val,
							},
						})
					}
					match = false
					continue
				}
			}
		}
		if checkKey(res, k) {
			*res = append(*res, models.HeaderResult{
				Normal: true,
				Expected: models.Header{
					Key:   k,
					Value: v,
				},
				Actual: models.Header{
					Key:   k,
					Value: val,
				},
			})
		}
	}
	for k, v := range h2 {
		regexArr, isNoisy := CheckStringExist(k, noise)
		if isNoisy && len(regexArr) != 0 {
			isNoisy, _ = MatchesAnyRegex(v[0], regexArr)
		}
		isNoisy = isNoisy || isHeaderNoisy
		val, ok := h1[k]
		if isNoisy && checkKey(res, k) {
			*res = append(*res, models.HeaderResult{
				Normal: true,
				Expected: models.Header{
					Key:   k,
					Value: val,
				},
				Actual: models.Header{
					Key:   k,
					Value: v,
				},
			})
			continue
		}
		if !ok {
			if checkKey(res, k) {
				*res = append(*res, models.HeaderResult{
					Normal: false,
					Expected: models.Header{
						Key:   k,
						Value: nil,
					},
					Actual: models.Header{
						Key:   k,
						Value: v,
					},
				})
			}

			match = false
		}
	}
	return match
}

func MapToArray(mp map[string][]string) []string {
	var result []string
	for k := range mp {
		result = append(result, k)
	}
	return result
}

func CheckStringExist(s string, mp map[string][]string) ([]string, bool) {
	if val, ok := mp[s]; ok {
		return val, ok
	}
	ok, val := MatchesAnyRegex(s, MapToArray(mp))
	if ok {
		return mp[val], ok
	}
	return []string{}, false
}

func MatchesAnyRegex(str string, regexArray []string) (bool, string) {
	for _, pattern := range regexArray {
		re := regexp.MustCompile(pattern)
		if re.MatchString(str) {
			return true, pattern
		}
	}
	return false, ""
}

func AddHTTPBodyToMap(body string, m map[string][]string) error {
	// add body
	if json.Valid([]byte(body)) {
		var result interface{}

		err := json.Unmarshal([]byte(body), &result)
		if err != nil {
			return err
		}
		j := Flatten(result)
		for k, v := range j {
			nk := "body"
			if k != "" {
				nk = nk + "." + k
			}
			m[nk] = v
		}
	} else {
		// add it as raw text
		m["body"] = []string{body}
	}
	return nil
}

// Flatten takes a map and returns a new one where nested maps are replaced
// by dot-delimited keys.
// examples of valid jsons - https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/JSON/parse#examples
func Flatten(j interface{}) map[string][]string {
	if j == nil {
		return map[string][]string{"": {""}}
	}
	o := make(map[string][]string)
	x := reflect.ValueOf(j)
	switch x.Kind() {
	case reflect.Map:
		m, ok := j.(map[string]interface{})
		if !ok {
			return map[string][]string{}
		}
		for k, v := range m {
			nm := Flatten(v)
			for nk, nv := range nm {
				fk := k
				if nk != "" {
					fk = fk + "." + nk
				}
				o[fk] = nv
			}
		}
	case reflect.Bool:
		o[""] = []string{strconv.FormatBool(x.Bool())}
	case reflect.Float64:
		o[""] = []string{strconv.FormatFloat(x.Float(), 'E', -1, 64)}
	case reflect.String:
		o[""] = []string{x.String()}
	case reflect.Slice:
		child, ok := j.([]interface{})
		if !ok {
			return map[string][]string{}
		}
		for _, av := range child {
			nm := Flatten(av)
			for nk, nv := range nm {
				if ov, exists := o[nk]; exists {
					o[nk] = append(ov, nv...)
				} else {
					o[nk] = nv
				}
			}
		}
	}
	return o
}
