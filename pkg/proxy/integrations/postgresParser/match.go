package postgresparser

import (
	"encoding/base64"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgproto3/v2"
	"go.keploy.io/server/pkg/hooks"
	"go.keploy.io/server/pkg/models"
	"go.keploy.io/server/pkg/proxy/util"
	"go.uber.org/zap"
)

func matchingReadablePG(requestBuffers [][]byte, logger *zap.Logger, h *hooks.Hook, ConnectionId string, recorded_prep PrepMap) (bool, []models.Frontend, error) {
	reqGoingOn := decodePgRequest(requestBuffers[0], logger)
	if reqGoingOn != nil {
		fmt.Println("PacketTypes", reqGoingOn.PacketTypes)
		fmt.Println("On going req .....", reqGoingOn)
		fmt.Println("ConnectionId-", ConnectionId)
		fmt.Println("TestMap*****", testmap)
	}
	for {
		tcsMocks, err := h.GetConfigMocks()
		if err != nil {
			return false, nil, fmt.Errorf("error while getting tcs mocks %v", err)
		}

		var isMatched, sortFlag bool = false, true
		var sortedTcsMocks []*models.Mock
		var matchedMock *models.Mock

		for _, mock := range tcsMocks {
			if mock == nil {
				continue
			}

			if sortFlag {
				if !mock.TestModeInfo.IsFiltered {
					sortFlag = false
				} else {
					sortedTcsMocks = append(sortedTcsMocks, mock)
				}
			}

			initMock := *mock
			if len(mock.Spec.PostgresRequests) == len(requestBuffers) {
				for requestIndex, reqBuff := range requestBuffers {
					bufStr := base64.StdEncoding.EncodeToString(reqBuff)
					decodePgRequest(reqBuff, logger)
					encodedMock, err := PostgresDecoderBackend(mock.Spec.PostgresRequests[requestIndex])
					if err != nil {
						logger.Debug("Error while decoding postgres request", zap.Error(err))
					}

					if bufStr == "AAAACATSFi8=" {
						ssl := models.Frontend{
							Payload: "Tg==",
						}
						return true, []models.Frontend{ssl}, nil
					}

					if mock.Spec.PostgresRequests[requestIndex].Identfier == "StartupRequest" && isStartupPacket(reqBuff) && mock.Spec.PostgresRequests[requestIndex].Payload != "AAAACATSFi8=" && mock.Spec.PostgresResponses[requestIndex].AuthType == 10 {
						logger.Debug("CHANGING TO MD5 for Response", zap.String("mock", mock.Name), zap.String("Req", bufStr))
						initMock.Spec.PostgresResponses[requestIndex].AuthType = 5
						return true, initMock.Spec.PostgresResponses, nil
					} else {
						if len(encodedMock) > 0 && encodedMock[0] == 'p' && mock.Spec.PostgresRequests[requestIndex].PacketTypes[0] == "p" && reqBuff[0] == 'p' {
							logger.Debug("CHANGING TO MD5 for Request and Response", zap.String("mock", mock.Name), zap.String("Req", bufStr))

							initMock.Spec.PostgresRequests[requestIndex].PasswordMessage.Password = "md5fe4f2f657f01fa1dd9d111d5391e7c07"

							initMock.Spec.PostgresResponses[requestIndex].PacketTypes = []string{"R", "S", "S", "S", "S", "S", "S", "S", "S", "S", "S", "S", "K", "Z"}
							initMock.Spec.PostgresResponses[requestIndex].AuthType = 0
							initMock.Spec.PostgresResponses[requestIndex].BackendKeyData = pgproto3.BackendKeyData{
								ProcessID: 2613,
								SecretKey: 824670820,
							}
							initMock.Spec.PostgresResponses[requestIndex].ReadyForQuery.TxStatus = 73
							initMock.Spec.PostgresResponses[requestIndex].ParameterStatusCombined = []pgproto3.ParameterStatus{
								{
									Name:  "application_name",
									Value: "",
								},
								{
									Name:  "client_encoding",
									Value: "UTF8",
								},
								{
									Name:  "DateStyle",
									Value: "ISO, MDY",
								},
								{
									Name:  "integer_datetimes",
									Value: "on",
								},
								{
									Name:  "IntervalStyle",
									Value: "postgres",
								},
								{
									Name:  "is_superuser",
									Value: "UTF8",
								},
								{
									Name:  "server_version",
									Value: "13.12 (Debian 13.12-1.pgdg120+1)",
								},
								{
									Name:  "session_authorization",
									Value: "keploy-user",
								},
								{
									Name:  "standard_conforming_strings",
									Value: "on",
								},
								{
									Name:  "TimeZone",
									Value: "Etc/UTC",
								},
								{
									Name:  "TimeZone",
									Value: "Etc/UTC",
								},
							}
							return true, initMock.Spec.PostgresResponses, nil
						}
					}

				}
			}
			// maintain test prepare statement map for each connection id
			getTestPS(requestBuffers, logger, ConnectionId)
		}

		logger.Debug("Sorted Mocks: ", zap.Any("Len of sortedTcsMocks", len(sortedTcsMocks)))

		isSorted := false
		var idx int
		if !isMatched {
			//use findBinaryMatch twice one for sorted and another for unsorted
			// give more priority to sorted like if you find more than 0.5 in sorted then return that
			if len(sortedTcsMocks) > 0 {
				isSorted = true
				idx1, newMock := findPGStreamMatch(sortedTcsMocks, requestBuffers, logger, h, isSorted, ConnectionId, recorded_prep)
				if idx1 != -1 {
					isMatched = true
					matchedMock = tcsMocks[idx1]
					if newMock != nil {
						matchedMock = newMock
					}
					fmt.Println("Matched In Absolute Custom Matching for sorted!!!", matchedMock.Name)
				}
				idx = findBinaryStreamMatch(sortedTcsMocks, requestBuffers, logger, h, isSorted)
				if idx != -1 && !isMatched {
					isMatched = true
					matchedMock = tcsMocks[idx]
					fmt.Println("Matched In Binary Matching for sorted!!!", matchedMock.Name)
				}
			}
		}

		if !isMatched {
			isSorted = false
			idx1, newMock := findPGStreamMatch(tcsMocks, requestBuffers, logger, h, isSorted, ConnectionId, recorded_prep)
			if idx1 != -1 {
				isMatched = true
				matchedMock = tcsMocks[idx1]
				if newMock != nil {
					matchedMock = newMock
				}
				fmt.Println("Matched In Absolute Custom Matching for Unsorted", matchedMock.Name)
			}
			idx = findBinaryStreamMatch(tcsMocks, requestBuffers, logger, h, isSorted)
			if idx != -1 && !isMatched {
				isMatched = true
				matchedMock = tcsMocks[idx]

				fmt.Println("Matched In Binary Matching for Unsorted", matchedMock.Name)
			}
		}

		if isMatched {
			logger.Info("Matched mock", zap.String("mock", matchedMock.Name))
			if matchedMock.TestModeInfo.IsFiltered {
				originalMatchedMock := *matchedMock
				matchedMock.TestModeInfo.IsFiltered = false
				matchedMock.TestModeInfo.SortOrder = math.MaxInt
				isUpdated := h.UpdateConfigMock(&originalMatchedMock, matchedMock)
				if !isUpdated {
					continue
				}
			}
			return true, matchedMock.Spec.PostgresResponses, nil
		}

		break
	}
	return false, nil, nil
}

func FuzzyCheck(encoded, reqBuff []byte) float64 {
	k := util.AdaptiveK(len(reqBuff), 3, 8, 5)
	shingles1 := util.CreateShingles(encoded, k)
	shingles2 := util.CreateShingles(reqBuff, k)
	similarity := util.JaccardSimilarity(shingles1, shingles2)
	return similarity
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func PreparedStatementMatch(mock *models.Mock, actualPgReq *models.Backend, logger *zap.Logger, h *hooks.Hook, ConnectionId string, recorded_prep PrepMap) (bool, []string, error) {
	// fmt.Println("Inside PreparedStatementMatch")
	// check the current Query associated with the connection id and Identifier
	ifps := checkIfps(actualPgReq.PacketTypes)
	if !ifps {
		return false, nil, nil
	}
	// check if given mock is a prepared statement
	ifpsMock := checkIfps(mock.Spec.PostgresRequests[0].PacketTypes)
	if !ifpsMock {
		return false, nil, nil
	}

	if len(mock.Spec.PostgresRequests[0].PacketTypes) != len(actualPgReq.PacketTypes) {
		return false, nil, nil
	}

	// get all the binds from the actualPgReq
	binds := actualPgReq.Binds
	newBinPreparedStatement := make([]string, 0)
	mockBinds := mock.Spec.PostgresRequests[0].Binds
	mockConn := mock.ConnectionId
	var foo bool = false
	for idx, bind := range binds {
		current_ps := bind.PreparedStatement
		current_querydata := testmap[ConnectionId]
		current_query := ""
		// check in the map that what's the current query for this preparedstatement
		// then will check what is the recorded prepared statement for this query
		for _, v := range current_querydata {
			if v.PrepIdentifier == current_ps {
				// fmt.Println("Current query for this identifier is ", v.Query)
				current_query = v.Query
				break
			}
		}
		fmt.Println("Current Query is ", current_query, "for mock", mock.Name, "and connectionId is", ConnectionId, "and current identifier is", current_ps, "FOR QUERY", current_query)
		foo = false
		if mock.Name == "mock-222" {
			fmt.Println("mockBinds", mockBinds)
		}
		// for _, mb := range mockBinds {
		// check if the query for mock ps (v.PreparedStatement) is same as the current query
		for _, querydata := range recorded_prep[mockConn] {
			if querydata.Query == current_query && mockBinds[idx].PreparedStatement == querydata.PrepIdentifier {
				fmt.Println("Matched with the recorded prepared statement with Identifier and connectionID is", querydata.PrepIdentifier, ", conn- ", mockConn, "and current identifier is", current_ps, "FOR QUERY", current_query)
				foo = true
				break
			}
			// }
		}
	}
	if foo {
		return true, newBinPreparedStatement, nil
	}
	// if len(newBinPreparedStatement) > 0 && len(binds) == len(newBinPreparedStatement) {
	// 	return true, newBinPreparedStatement, nil
	// }
	return false, nil, nil

	// check what was the prepared statement recorded
	// old_ps := ""
	// for _, ps := range recorded_prep {
	// 	for _, v := range ps {
	// 		if current_query == v.Query && current_ps != v.PrepIdentifier {
	// 			// fmt.Println("Matched with the recorded prepared statement with Identifier and connectionID is", v.PrepIdentifier, ", conn- ", conn, "and current identifier is", current_ps, "FOR QUERY", current_query)
	// 			// fmt.Println("MOCK NUMBER IS ", mock.Name)
	// 			current_ps = v.PrepIdentifier
	// 			break
	// 		}
	// 	}
	// }

	// if strings.Contains(current_ps, "S_") && current_ps != "" {
	// 	newBinPreparedStatement = append(newBinPreparedStatement, current_ps)
	// }
	// }

}

func compareExactMatch(mock *models.Mock, actualPgReq *models.Backend, logger *zap.Logger, h *hooks.Hook, ConnectionId string, isSorted bool, recorded_prep PrepMap) (bool, error) {

	// fmt.Println("Inside Compare Exact Match", len(reqBuff), string(reqBuff), "MOCK NAME - ", mock.Name)
	// actualPgReq := decodePgRequest(reqBuff, logger)
	// if actualPgReq == nil {
	// 	return false, nil
	// }
	// if mock.Name == "mock-196" || mock.Name == "mock-197" || mock.Name == "mock-198" || mock.Name == "mock-199" || mock.Name == "mock-204" {
	// 	fmt.Println("Inside Compare Exact Match", actualPgReq, "MOCK NAME - ", mock.Name, "WITH CONN ID", ConnectionId)
	// }

	// have to ignore first parse message of begin read only
	// should compare only query in the parse message
	if len(actualPgReq.PacketTypes) != len(mock.Spec.PostgresRequests[0].PacketTypes) {
		//check for begin read only
		// if math.Abs(float64(len(actualPgReq.PacketTypes))-float64(len(mock.Spec.PostgresRequests[0].PacketTypes))) == 1 {
		// 	if len(actualPgReq.PacketTypes) > 0 && len(mock.Spec.PostgresRequests[0].PacketTypes) > 0 {
		// 		if mock.Spec.PostgresRequests[0].PacketTypes[0] == "P" && mock.Spec.PostgresRequests[0].Parses[0].Query == "BEGIN READ ONLY" {
		// 			if mock.Spec.PostgresRequests[0].Parses[1].Query == actualPgReq.Parses[0].Query {
		// 				sliceCommandTag(mock, logger, testmap[ConnectionId], actualPgReq, false)
		// 				return true, nil
		// 			}
		// 		}
		// 	}
		// }

		// if isSorted && len(actualPgReq.PacketTypes) > 0 && len(mock.Spec.PostgresRequests[0].PacketTypes) > 0 {
		// 	if mock.Spec.PostgresRequests[0].PacketTypes[0] == "P" && actualPgReq.PacketTypes[0] == "B" && mock.Name == "mock-197" {
		// 		// is_prep := checkIfps(actualPgReq.PacketTypes)
		// 		// if is_prep {
		// 		// 	// fmt.Println("Inside Prepared Statement")
		// 		// 	// sliceCommandTag(mock, logger, testmap[ConnectionId], actualPgReq, true)
		// 		// }

		// 		return true, nil
		// 	}
		// }
		return false, nil
	}

	// call a separate function for matching prepared statements
	for idx, v := range actualPgReq.PacketTypes {
		if v != mock.Spec.PostgresRequests[0].PacketTypes[idx] {
			return false, nil
		}
	}
	// IsPreparedStatement(mock, actualPgReq, logger, ConnectionId)

	// this will give me the
	var (
		p, b, e int = 0, 0, 0
	)
	for i := 0; i < len(actualPgReq.PacketTypes); i++ {
		switch actualPgReq.PacketTypes[i] {
		case "P":
			// fmt.Println("Inside P")
			p++
			if actualPgReq.Parses[p-1].Name != mock.Spec.PostgresRequests[0].Parses[p-1].Name {
				return false, nil
			}
			if actualPgReq.Parses[p-1].Query != mock.Spec.PostgresRequests[0].Parses[p-1].Query {
				return false, nil
			}
			if len(actualPgReq.Parses[p-1].ParameterOIDs) != len(mock.Spec.PostgresRequests[0].Parses[p-1].ParameterOIDs) {
				return false, nil
			}
			for j := 0; j < len(actualPgReq.Parses[p-1].ParameterOIDs); j++ {
				if actualPgReq.Parses[p-1].ParameterOIDs[j] != mock.Spec.PostgresRequests[0].Parses[p-1].ParameterOIDs[j] {
					return false, nil
				}
			}

		case "B":
			// fmt.Println("Inside B")
			b++
			if actualPgReq.Binds[b-1].DestinationPortal != mock.Spec.PostgresRequests[0].Binds[b-1].DestinationPortal {
				return false, nil
			}
			// if Is Prep statement true hai to wo se jo aya hai usko S_ identifier ko and connection Id ko compare karo
			// if is_prep && len(newBindPs) > 0 {
			// 	// fmt.Println("New Bind Prepared Statement", newBindPs, "for mock", mock.Name)
			// 	if mock.Spec.PostgresRequests[0].Binds[b-1].PreparedStatement != newBindPs[b-1] {
			// 		return false, nil
			// 	}
			// } else {
			if actualPgReq.Binds[b-1].PreparedStatement != mock.Spec.PostgresRequests[0].Binds[b-1].PreparedStatement {
				return false, nil
			}
			// }

			if len(actualPgReq.Binds[b-1].ParameterFormatCodes) != len(mock.Spec.PostgresRequests[0].Binds[b-1].ParameterFormatCodes) {
				return false, nil
			}
			for j := 0; j < len(actualPgReq.Binds[b-1].ParameterFormatCodes); j++ {
				if actualPgReq.Binds[b-1].ParameterFormatCodes[j] != mock.Spec.PostgresRequests[0].Binds[b-1].ParameterFormatCodes[j] {
					return false, nil
				}
			}
			if len(actualPgReq.Binds[b-1].Parameters) != len(mock.Spec.PostgresRequests[0].Binds[b-1].Parameters) {
				return false, nil
			}
			for j := 0; j < len(actualPgReq.Binds[b-1].Parameters); j++ {
				for _, v := range actualPgReq.Binds[b-1].Parameters[j] {
					if v != mock.Spec.PostgresRequests[0].Binds[b-1].Parameters[j][0] {
						return false, nil
					}
				}
			}
			if len(actualPgReq.Binds[b-1].ResultFormatCodes) != len(mock.Spec.PostgresRequests[0].Binds[b-1].ResultFormatCodes) {
				return false, nil
			}
			for j := 0; j < len(actualPgReq.Binds[b-1].ResultFormatCodes); j++ {
				if actualPgReq.Binds[b-1].ResultFormatCodes[j] != mock.Spec.PostgresRequests[0].Binds[b-1].ResultFormatCodes[j] {
					return false, nil
				}
			}

		case "E":
			// fmt.Println("Inside E")
			e++
			if actualPgReq.Executes[e-1].Portal != mock.Spec.PostgresRequests[0].Executes[e-1].Portal {
				return false, nil
			}
			if actualPgReq.Executes[e-1].MaxRows != mock.Spec.PostgresRequests[0].Executes[e-1].MaxRows {
				return false, nil
			}
		// case "d":
		// 	if actualPgReq.CopyData.Data != mock.Spec.PostgresRequests[0].CopyData.Data {
		// 		return false, nil
		// 	}
		case "c":
			if actualPgReq.CopyDone != mock.Spec.PostgresRequests[0].CopyDone {
				return false, nil
			}
		case "H":
			if actualPgReq.CopyFail.Message != mock.Spec.PostgresRequests[0].CopyFail.Message {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

var testmap TestPrepMap

func getTestPS(reqBuff [][]byte, logger *zap.Logger, ConnectionId string) {
	// maintain a map of current prepared statements and their corresponding connection id
	// if it's the prepared statement match the query with the recorded prepared statement and return the response of that matched prepared statement at that connection
	// so if parse is coming save to a same map
	actualPgReq := decodePgRequest(reqBuff[0], logger)
	if actualPgReq == nil {
		return
	}
	testmap2 := make(TestPrepMap)
	if testmap != nil {
		testmap2 = testmap
	}
	querydata := make([]QueryData, 0)
	if len(actualPgReq.PacketTypes) > 0 && actualPgReq.PacketTypes[0] != "p" && actualPgReq.Identfier != "StartupRequest" {
		p := 0
		for _, header := range actualPgReq.PacketTypes {
			if header == "P" {
				if strings.Contains(actualPgReq.Parses[p].Name, "S_") && !IsValuePresent(ConnectionId, actualPgReq.Parses[p].Name) {
					querydata = append(querydata, QueryData{PrepIdentifier: actualPgReq.Parses[p].Name, Query: actualPgReq.Parses[p].Query})
				}
				p++
			}
		}
	}

	// also append the query data for the prepared statement
	if len(querydata) > 0 {
		testmap2[ConnectionId] = append(testmap2[ConnectionId], querydata...)
		// fmt.Println("Test Prepared statement Map", testmap2)
		testmap = testmap2
	}

}

func IsValuePresent(connectionid string, value string) bool {
	if testmap != nil {
		for _, v := range testmap[connectionid] {
			if v.PrepIdentifier == value {
				return true
			}
		}
	}
	return false
}

func findPGStreamMatch(tcsMocks []*models.Mock, requestBuffers [][]byte, logger *zap.Logger, h *hooks.Hook, isSorted bool, connectionId string, recorded_prep PrepMap) (int, *models.Mock) {

	mxIdx := -1
	// matchedMockConnectionId := "y"

	// var isPrepareStatementMapMatched bool
	match := false
	// loop for the exact match of the request
	for idx, mock := range tcsMocks {
		if len(mock.Spec.PostgresRequests) == len(requestBuffers) {
			for _, reqBuff := range requestBuffers {
				actualPgReq := decodePgRequest(reqBuff, logger)
				if actualPgReq == nil {
					return -1, nil
				}

				// here handle cases of prepared statement very carefully
				match, err := compareExactMatch(mock, actualPgReq, logger, h, connectionId, isSorted, recorded_prep)
				if err != nil {
					logger.Error("Error while matching exact match", zap.Error(err))
					continue
				}
				if match {
					return idx, nil
				}
			}
		}
	}
	if !isSorted {
		return mxIdx, nil
	}
	// loop for the ps match of the request
	if !match {
		for idx, mock := range tcsMocks {
			if len(mock.Spec.PostgresRequests) == len(requestBuffers) {
				for _, reqBuff := range requestBuffers {
					actualPgReq := decodePgRequest(reqBuff, logger)
					if actualPgReq == nil {
						return -1, nil
					}
					// just matching the corresponding PS in this case there is no need to edit the mock
					match, newBindPs, err := PreparedStatementMatch(mock, actualPgReq, logger, h, connectionId, recorded_prep)
					if err != nil {
						logger.Error("Error while matching prepared statements", zap.Error(err))
					}

					if match {
						fmt.Println("New Bind Prepared Statement", newBindPs, "for mock", mock.Name)
						fmt.Println(actualPgReq.Binds[0].PreparedStatement)
						fmt.Println(actualPgReq.Binds[0].Parameters)
						fmt.Println(actualPgReq.Binds[0].ParameterFormatCodes)
						fmt.Println(actualPgReq.Binds[0].ResultFormatCodes)
						return idx, nil
					}
				}
			}
		}
	}

	if !match {
		for idx, mock := range tcsMocks {
			if len(mock.Spec.PostgresRequests) == len(requestBuffers) {
				for _, reqBuff := range requestBuffers {
					actualPgReq := decodePgRequest(reqBuff, logger)
					if actualPgReq == nil {
						return -1, nil
					}

					// logRangeofMocks(mock, 254, 261, actualPgReq, logger, connectionId)

					// have to ignore first parse message of begin read only
					// should compare only query in the parse message
					if len(actualPgReq.PacketTypes) != len(mock.Spec.PostgresRequests[0].PacketTypes) {
						//check for begin read only
						if len(actualPgReq.PacketTypes) > 0 && len(mock.Spec.PostgresRequests[0].PacketTypes) > 0 {

							ischanged, newMock := changeResToPS(mock, actualPgReq, logger, connectionId)

							if ischanged {
								return idx, newMock
							} else {
								continue
							}
						}

					}
				}
			}
		}
	}

	return mxIdx, nil
}

// check what are the queries for the given ps of actualPgReq
// check if the execute query is present for that or not
// mark that mock true and return the response by changing the res format like
// postgres data types acc to result set format
func changeResToPS(mock *models.Mock, actualPgReq *models.Backend, logger *zap.Logger, connectionId string) (bool, *models.Mock) {
	packets := actualPgReq.PacketTypes
	mockPackets := mock.Spec.PostgresRequests[0].PacketTypes

	// [P, B, E, P, B, D, E] => [B, E, B, E]
	// write code that of packet is ["B", "E"] and mockPackets ["P", "B", "D", "E"] handle it in case1
	// and if packet is [B, E, B, E] and mockPackets [P, B, E, P, B, D, E] handle it in case2

	ischanged := false
	var newMock *models.Mock
	// [P, B, E, P, B, D, E] -> [B, E, P, B, D, E] like mock - 196
	if reflect.DeepEqual(packets, []string{"B", "E", "P", "B", "D", "E"}) && reflect.DeepEqual(mockPackets, []string{"P", "B", "E", "P", "B", "D", "E"}) {
		fmt.Println("Handling Case 1 for mock", mock.Name)
		// handleCase1(packets, mockPackets)
		// also check if the second query is same or not
		if actualPgReq.Parses[0].Query != mock.Spec.PostgresRequests[0].Parses[0].Query {
			return false, nil
		}

		newMock = sliceCommandTag(mock, logger, testmap[connectionId], actualPgReq, 1)
		return true, newMock
	}

	// case 2
	var ps string
	if reflect.DeepEqual(packets, []string{"B", "E"}) && reflect.DeepEqual(mockPackets, []string{"P", "B", "D", "E"}) {
		fmt.Println("Handling Case 2 for mock", mock.Name)
		ps = actualPgReq.Binds[0].PreparedStatement
		for _, v := range testmap[connectionId] {
			if v.Query == mock.Spec.PostgresRequests[0].Parses[0].Query && v.PrepIdentifier == ps {
				ischanged = true
				break
			}
		}
	}

	if ischanged {
		if strings.Contains(ps, "S_") {
			fmt.Println("Inside Prepared Statement")
			newMock = sliceCommandTag(mock, logger, testmap[connectionId], actualPgReq, 2)
		}
		return true, newMock
	}

	// packets = []string{"B", "E", "B", "E"}
	// mockPackets = []string{"P", "B", "E", "P", "B", "D", "E"}

	// Case 3
	if reflect.DeepEqual(packets, []string{"B", "E", "B", "E"}) && reflect.DeepEqual(mockPackets, []string{"P", "B", "E", "P", "B", "D", "E"}) {
		fmt.Println("Handling Case 3 for mock", mock.Name)
		ischanged1 := false
		ps1 := actualPgReq.Binds[0].PreparedStatement
		for _, v := range testmap[connectionId] {
			if v.Query == mock.Spec.PostgresRequests[0].Parses[0].Query && v.PrepIdentifier == ps1 {
				ischanged1 = true
				break
			}
		}
		ischanged2 := false
		ps2 := actualPgReq.Binds[1].PreparedStatement
		for _, v := range testmap[connectionId] {
			if v.Query == mock.Spec.PostgresRequests[0].Parses[1].Query && v.PrepIdentifier == ps2 {
				ischanged2 = true
				break
			}
		}
		if ischanged1 && ischanged2 {
			newMock = sliceCommandTag(mock, logger, testmap[connectionId], actualPgReq, 2)
			return true, newMock
		}

	}

	return false, nil

}

func logRangeofMocks(mock *models.Mock, first, second int, actualPgReq *models.Backend, logger *zap.Logger, connectionId string) {

	mockNum := mock.Name
	mockNum = mockNum[5:]
	num, err := strconv.Atoi(mockNum)
	if err != nil {
		return
	}
	if num >= first && num <= second {

		fmt.Println("------", mock.Name, "------")
		fmt.Println("PACKETS", actualPgReq.PacketTypes, "MOCK PACKETS", actualPgReq.PacketTypes)
		fmt.Println("ActualPgReq", actualPgReq, "MOCK REQ", mock.Spec.PostgresRequests[0])
		fmt.Println("TestMap", testmap)
		fmt.Println("ConnectionId ⚡⚡⚡⚡⚡", connectionId)
		fmt.Println("-------------------------------")
	}
}