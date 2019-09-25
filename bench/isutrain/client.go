package isutrain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chibiegg/isucon9-final/bench/internal/bencherror"
	"github.com/chibiegg/isucon9-final/bench/internal/config"
	"github.com/chibiegg/isucon9-final/bench/internal/endpoint"
	"github.com/chibiegg/isucon9-final/bench/internal/util"
	"github.com/morikuni/failure"
	"go.uber.org/zap"
)

var (
	ErrTrainSeatsNotFound = errors.New("列車の座席が見つかりませんでした")
)

type ClientOption struct {
	WantStatusCode int
}

type Client struct {
	sess    *Session
	baseURL *url.URL
}

func NewClient() (*Client, error) {
	sess, err := NewSession()
	if err != nil {
		return nil, bencherror.NewCriticalError(err, "Isutrainクライアントが作成できません. 運営に確認をお願いいたします")
	}

	u, err := url.Parse(config.TargetBaseURL)
	if err != nil {
		return nil, bencherror.NewCriticalError(err, "Isutrainクライアントが作成できません. 運営に確認をお願いいたします")
	}

	return &Client{
		sess:    sess,
		baseURL: u,
	}, nil
}

func NewClientForInitialize() (*Client, error) {
	sess, err := newSessionForInitialize()
	if err != nil {
		return nil, bencherror.NewCriticalError(err, "Isutrainクライアントが作成できません. 運営に確認をお願いいたします")
	}

	u, err := url.Parse(config.TargetBaseURL)
	if err != nil {
		return nil, bencherror.NewCriticalError(err, "Isutrainクライアントが作成できません. 運営に確認をお願いいたします")
	}

	return &Client{
		sess:    sess,
		baseURL: u,
	}, nil
}

// ReplaceMockTransport は、clientの利用するhttp.RoundTripperを、DefaultTransportに差し替えます
// NOTE: httpmockはhttp.DefaultTransportを利用するため、モックテストの時この関数を利用する
func (c *Client) ReplaceMockTransport() {
	c.sess.httpClient.Transport = http.DefaultTransport
}

func (c *Client) Initialize(ctx context.Context) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Initialize)
	u.Path = filepath.Join(u.Path, endpointPath)

	ctx, cancel := context.WithTimeout(ctx, config.InitializeTimeout)
	defer cancel()

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		bencherror.InitializeErrs.AddError(bencherror.NewApplicationError(err, "POST %s: リクエストに失敗しました", endpointPath))
		return
	}

	// TODO: 言語を返すようにしたり、キャンペーンを返すようにする場合、ちゃんと設定されていなかったらFAILにする
	resp, err := c.sess.do(req)
	if err != nil {
		bencherror.InitializeErrs.AddError(bencherror.NewApplicationError(err, "POST %s: リクエストに失敗しました", endpointPath))
		return
	}
	defer resp.Body.Close()

	if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
		bencherror.BenchmarkErrs.AddError(failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK)))
		return
	}

	// FIXME: 予約可能日数をレスポンスから受け取る
	if err := config.SetAvailReserveDays(30); err != nil {
		bencherror.InitializeErrs.AddError(bencherror.NewCriticalError(err, "POST %s: 予約可能日数の設定に失敗しました", endpointPath))
		return
	}

	endpoint.IncPathCounter(endpoint.Initialize)
}

func (c *Client) Settings(ctx context.Context) (*Settings, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Settings)
	u.Path = filepath.Join(u.Path, endpointPath)

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: 設定情報の取得に失敗しました", endpointPath))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: 設定情報の取得に失敗しました", endpointPath))
	}
	defer resp.Body.Close()

	if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))
	}
	// TODO: opts.WantStatusCodes制御

	var settings *Settings
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: レスポンスのUnmarshalに失敗しました", endpointPath))
	}

	return settings, nil
}

func (c *Client) Signup(ctx context.Context, email, password string, opts *ClientOption) error {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Signup)
	u.Path = filepath.Join(u.Path, endpointPath)

	b, err := json.Marshal(&User{
		Email:    email,
		Password: password,
	})
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), bytes.NewBuffer(b))
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.sess.do(req)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode))
		}
	}

	endpoint.IncPathCounter(endpoint.Signup)

	return nil
}

func (c *Client) Login(ctx context.Context, email, password string, opts *ClientOption) error {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Login)
	u.Path = filepath.Join(u.Path, endpointPath)

	b, err := json.Marshal(&User{
		Email:    email,
		Password: password,
	})
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), bytes.NewBuffer(b))
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.sess.do(req)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode))
		}
	}

	endpoint.IncPathCounter(endpoint.Login)

	return nil
}

func (c *Client) Logout(ctx context.Context, opts *ClientOption) error {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Logout)
	u.Path = filepath.Join(u.Path, endpointPath)
	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))

		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode))
		}
	}

	return nil
}

// ListStations は駅一覧列挙APIです
func (c *Client) ListStations(ctx context.Context, opts *ClientOption) ([]*Station, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.ListStations)
	u.Path = filepath.Join(u.Path, endpointPath)

	log.Printf("[ListStations] uri=%s\n", u.String())

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return []*Station{}, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return []*Station{}, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return []*Station{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return []*Station{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode))
		}
	}

	var stations []*Station
	if err := json.NewDecoder(resp.Body).Decode(&stations); err != nil {
		// FIXME: 実装
		return []*Station{}, failure.Wrap(err, failure.Messagef("GET %s: レスポンスのUnmarshalに失敗しました", endpointPath))
	}

	endpoint.IncPathCounter(endpoint.ListStations)

	return stations, nil
}

// SearchTrains は 列車検索APIです
func (c *Client) SearchTrains(ctx context.Context, useAt time.Time, from, to string, opts *ClientOption) (Trains, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.SearchTrains)
	u.Path = filepath.Join(u.Path, endpointPath)

	failureCtx := failure.Context{
		"use_at":      util.FormatISO8601(useAt),
		"train_class": "",
		"from":        from,
		"to":          to,
	}

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Trains{}, failure.Wrap(err, failure.Messagef("GET %s: 列車検索リクエストに失敗しました", endpointPath), failureCtx)
	}

	query := req.URL.Query()
	query.Set("use_at", util.FormatISO8601(useAt))
	query.Set("train_class", "") // FIXME: 列車種別
	query.Set("from", from)
	query.Set("to", to)
	req.URL.RawQuery = query.Encode()

	resp, err := c.sess.do(req)
	if err != nil {
		return Trains{}, failure.Wrap(err, failure.Messagef("GET %s: 列車検索リクエストに失敗しました", endpointPath), failureCtx)
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return Trains{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK), failureCtx)

		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return Trains{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK), failureCtx)
		}
	}

	var trains Trains
	if err := json.NewDecoder(resp.Body).Decode(&trains); err != nil {
		// FIXME: 実装
		return Trains{}, failure.Wrap(err, failure.Messagef("GET %s: レスポンスのUnmarshalに失敗しました", endpointPath), failureCtx)
	}

	endpoint.IncPathCounter(endpoint.SearchTrains)

	return trains, nil
}

func (c *Client) ListTrainSeats(ctx context.Context, date time.Time, trainClass, trainName string, carNum int, departure, arrival string, opts *ClientOption) (*TrainSeatSearchResponse, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.ListTrainSeats)
	u.Path = filepath.Join(u.Path, endpointPath)

	failureCtx := failure.Context{
		"date":        util.FormatISO8601(date),
		"train_class": trainClass,
		"train_name":  trainName,
		"car_number":  strconv.Itoa(carNum),
		"from":        departure,
		"to":          arrival,
	}
	lgr := zap.S()

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		lgr.Warnf("座席列挙 リクエスト作成に失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath), failureCtx)
	}

	query := req.URL.Query()
	query.Set("date", util.FormatISO8601(date))
	query.Set("train_class", trainClass)
	query.Set("train_name", trainName)
	query.Set("car_number", strconv.Itoa(carNum))
	query.Set("from", departure)
	query.Set("to", arrival)
	req.URL.RawQuery = query.Encode()

	lgr.Infow("座席列挙",
		"date", util.FormatISO8601(date),
		"train_class", trainClass,
		"train_name", trainName,
		"car_number", strconv.Itoa(carNum),
		"from", departure,
		"to", arrival,
	)

	resp, err := c.sess.do(req)
	if err != nil {
		lgr.Warnf("座席列挙リクエスト失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath), failureCtx)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		// NOTE: 検索結果が見つからないことはあるので、その場合はスルーするように実装
		return nil, failure.Wrap(err, failure.Messagef("GET %s: 検索結果が空です", endpointPath), failureCtx)
	}

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			lgr.Warnf("座席列挙 ステータスコードが不正: %+v", err)
			return nil, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", resp.StatusCode, http.StatusOK), failureCtx)
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			lgr.Warnf("座席列挙 ステータスコードが不正: %+v", err)
			return nil, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", resp.StatusCode, opts.WantStatusCode), failureCtx)
		}
	}

	var listTrainSeatsResp *TrainSeatSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&listTrainSeatsResp); err != nil {
		lgr.Warnf("座席列挙Unmarshal失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("GET %s: レスポンスのUnmarshalに失敗しました", endpointPath), failureCtx)
	}

	endpoint.IncPathCounter(endpoint.ListTrainSeats)

	return listTrainSeatsResp, nil
}

func (c *Client) Reserve(
	ctx context.Context,
	trainClass, trainName string,
	seatClass string,
	seats TrainSeats,
	departure, arrival string,
	useAt time.Time,
	carNum int,
	child, adult int,
	typ string,
	opts *ClientOption,
) (*ReservationResponse, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.Reserve)
	u.Path = filepath.Join(u.Path, endpointPath)

	failureCtx := failure.Context{
		"train_class": trainClass,
		"train_name":  trainName,
		"seat_class":  seatClass,
		"seats":       fmt.Sprintf("%+v", seats),
		"departure":   departure,
		"arrival":     arrival,
		"date":        util.FormatISO8601(useAt),
		"car_num":     fmt.Sprintf("%d", carNum),
		"child":       fmt.Sprintf("%d", child),
		"adult":       fmt.Sprintf("%d", adult),
		"type":        typ,
	}
	lgr := zap.S()

	b, err := json.Marshal(&ReservationRequest{
		TrainClass: trainClass,
		TrainName:  trainName,
		SeatClass:  seatClass,
		Seats:      seats,
		Departure:  departure,
		Arrival:    arrival,
		Date:       useAt,
		CarNum:     carNum,
		Child:      child,
		Adult:      adult,
		Type:       typ,
	})
	if err != nil {
		return nil, err
	}

	lgr.Infof("予約クエリ: %s", string(b))

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), bytes.NewBuffer(b))
	if err != nil {
		lgr.Warnf("予約リクエスト失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath), failureCtx)
	}

	// FIXME: csrfトークン検証
	// _, err = req.Cookie("csrf_token")
	// if err != nil {
	// 	return nil, failure.Wrap(err, failure.Message("POST /api/train/reservation: CSRFトークンが不正です"))
	// }

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.sess.do(req)
	if err != nil {
		lgr.Warnf("予約リクエスト失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath), failureCtx)
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			lgr.Warnf("予約リクエストのレスポンスステータス不正: %+v", err)
			return nil, failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK), failureCtx)
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			lgr.Warnf("予約リクエストのレスポンスステータス不正: %+v", err)
			return nil, failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode), failureCtx)
		}
	}

	var reservation *ReservationResponse
	if err := json.NewDecoder(resp.Body).Decode(&reservation); err != nil {
		lgr.Warnf("予約リクエストのUnmarshal失敗: %+v", err)
		return nil, failure.Wrap(err, failure.Messagef("POST %s: JSONのUnmarshalに失敗しました", endpointPath), failureCtx)
	}

	endpoint.IncPathCounter(endpoint.Reserve)
	if SeatAvailability(seatClass) != SaNonReserved {
		endpoint.AddExtraScore(endpoint.Reserve, config.ReservedSeatExtraScore)
	}

	return reservation, nil
}

func (c *Client) CommitReservation(ctx context.Context, reservationID int, cardToken string, opts *ClientOption) error {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.CommitReservation)
	u.Path = filepath.Join(u.Path, endpointPath)

	lgr := zap.S()
	failureCtx := failure.Context{
		"reservation_id": fmt.Sprintf("%s", reservationID),
		"card_token":     cardToken,
	}

	// FIXME: 一応構造体にする？
	lgr.Infow("予約確定処理",
		"reservation_id", reservationID,
		"card_token", cardToken,
	)

	b, err := json.Marshal(map[string]interface{}{
		"reservation_id": reservationID,
		"card_token":     cardToken,
	})
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: Marshalに失敗しました", endpointPath), failureCtx)
	}

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), bytes.NewBuffer(b))
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストの作成に失敗しました", endpointPath), failureCtx)
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", resp.StatusCode, http.StatusOK), failureCtx)
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です: got=%d, want=%d", resp.StatusCode, opts.WantStatusCode), failureCtx)
		}
	}

	endpoint.IncPathCounter(endpoint.CommitReservation)

	return nil
}

func (c *Client) ListReservations(ctx context.Context, opts *ClientOption) ([]*SeatReservation, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetPath(endpoint.ListReservations)
	u.Path = filepath.Join(u.Path, endpointPath)

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return []*SeatReservation{}, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました"))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return []*SeatReservation{}, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました"))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return []*SeatReservation{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, http.StatusOK))
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return []*SeatReservation{}, failure.Wrap(err, failure.Messagef("GET %s: ステータスコードが不正です: got=%d, want=%d", endpointPath, resp.StatusCode, opts.WantStatusCode))
		}
	}

	var reservations []*SeatReservation
	if err := json.NewDecoder(resp.Body).Decode(&reservations); err != nil {
		return []*SeatReservation{}, failure.Wrap(err, failure.Messagef("GET %s: 予約のMarshalに失敗しました", endpointPath))
	}

	endpoint.IncPathCounter(endpoint.ListReservations)

	return reservations, nil
}

func (c *Client) ShowReservation(ctx context.Context, reservationID int, opts *ClientOption) (*SeatReservation, error) {
	u := *c.baseURL
	endpointPath := endpoint.GetDynamicPath(endpoint.ShowReservation, reservationID)
	u.Path = filepath.Join(u.Path, endpointPath)

	failureCtx := failure.Context{
		"reservation_id": fmt.Sprintf("%d", reservationID),
	}

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath), failureCtx)
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: リクエストに失敗しました", endpointPath), failureCtx)
	}

	var reservation *SeatReservation
	if err := json.NewDecoder(resp.Body).Decode(&reservation); err != nil {
		return nil, failure.Wrap(err, failure.Messagef("GET %s: Unmarshalに失敗しました", endpointPath), failureCtx)
	}

	endpoint.IncDynamicPathCounter(endpoint.ShowReservation)

	return reservation, nil
}

func (c *Client) CancelReservation(ctx context.Context, reservationID int, opts *ClientOption) error {
	u := *c.baseURL
	endpointPath := endpoint.GetDynamicPath(endpoint.CancelReservation, reservationID)
	u.Path = filepath.Join(u.Path, endpointPath)

	failureCtx := failure.Context{
		"reservation_id": fmt.Sprintf("%d", reservationID),
	}

	req, err := c.sess.newRequest(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath, failureCtx))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return failure.Wrap(err, failure.Messagef("POST %s: リクエストに失敗しました", endpointPath, failureCtx))
	}
	defer resp.Body.Close()

	if opts == nil {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です", endpointPath, resp.StatusCode, http.StatusOK), failureCtx)
		}
	} else {
		if err := bencherror.NewHTTPStatusCodeError(req, resp, opts.WantStatusCode); err != nil {
			return failure.Wrap(err, failure.Messagef("POST %s: ステータスコードが不正です", endpointPath, resp.StatusCode, opts.WantStatusCode), failureCtx)
		}
	}

	endpoint.IncDynamicPathCounter(endpoint.CancelReservation)

	return nil
}

func (c *Client) DownloadAsset(ctx context.Context, path string) ([]byte, error) {
	u := *c.baseURL
	u.Path = filepath.Join(u.Path, path)

	req, err := c.sess.newRequest(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return []byte{}, bencherror.PreTestErrs.AddError(bencherror.NewCriticalError(err, "GET %s: 静的ファイルのダウンロードに失敗しました", path))
	}

	resp, err := c.sess.do(req)
	if err != nil {
		return []byte{}, bencherror.PreTestErrs.AddError(bencherror.NewCriticalError(err, "GET %s: 静的ファイルのダウンロードに失敗しました", path))
	}
	defer resp.Body.Close()

	if err := bencherror.NewHTTPStatusCodeError(req, resp, http.StatusOK); err != nil {
		return []byte{}, bencherror.PreTestErrs.AddError(bencherror.NewCriticalError(err, "GET %s: ステータスコードが不正です", path))
	}

	return ioutil.ReadAll(resp.Body)
}
