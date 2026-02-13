package main

import (
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "modernc.org/sqlite"
)

//go:embed templates/*
var embeddedFiles embed.FS

// ──────────────────────────────────────────────
// Types
// ──────────────────────────────────────────────

type Config struct {
	TelegramToken string `json:"telegram_token"`
	AdminChatID   int64  `json:"admin_chat_id"`
	Port          string `json:"port"`
	DBPath        string `json:"db_path"`
}

type Transaction struct {
	EnrolmentNoDate    string
	TotalAmountCharged float64
	GSTAmount          float64
	OperatorID         string
	ResidentName       string
	UploadDate         string
}

type SlabCounts struct {
	Count125 int
	Count75  int
	Count0   int
}

type HistoryRow struct {
	Period   string
	Revenue  float64
	GST      float64
	Count125 int
	Count75  int
	Count0   int
	Total    int
}

type UploadResult struct {
	Filename string
	Rows     int
	Revenue  float64
	GST      float64
	Slabs    SlabCounts
}

// ──────────────────────────────────────────────
// Globals
// ──────────────────────────────────────────────

var (
	db     *sql.DB
	bot    *tgbotapi.BotAPI
	config Config
	mu     sync.Mutex
)

// ──────────────────────────────────────────────
// Main
// ──────────────────────────────────────────────

func main() {
	headless := flag.Bool("headless", false, "Run without opening browser")
	flag.Parse()

	loadConfig()
	initDB()
	defer db.Close()
	initBot()

	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/reset", handleReset)
	http.HandleFunc("/history", handleHistory)
	http.HandleFunc("/api/analytics", handleAnalyticsAPI)
	http.HandleFunc("/api/history", handleHistoryAPI)
	http.HandleFunc("/settings", handleSettings)
	http.HandleFunc("/save-settings", handleSaveSettings)

	fmt.Println("──────────────────────────────────────")
	fmt.Printf("  TollVault on port %s\n", config.Port)
	fmt.Printf("  http://localhost:%s\n", config.Port)
	if *headless {
		fmt.Println("  Mode: headless (no browser)")
	}
	fmt.Println("──────────────────────────────────────")

	if bot != nil {
		go handleTelegramUpdates()
	}

	if !*headless {
		go openBrowser("http://localhost:" + config.Port)
	}

	log.Fatal(http.ListenAndServe(":"+config.Port, nil))
}

// ──────────────────────────────────────────────
// Browser Auto-Open
// ──────────────────────────────────────────────

func openBrowser(url string) {
	time.Sleep(500 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

// ──────────────────────────────────────────────
// Configuration
// ──────────────────────────────────────────────

func loadConfig() {
	config = Config{Port: "8080", DBPath: "data.db"}
	f, err := os.Open("config.json")
	if err == nil {
		defer f.Close()
		json.NewDecoder(f).Decode(&config)
	} else if os.IsNotExist(err) {
		// Auto-generate config.json with defaults on first run
		saveConfigToDisk()
		log.Println("Created default config.json")
	}
}

func saveConfigToDisk() {
	f, err := os.Create("config.json")
	if err != nil {
		log.Println("Error saving config:", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(config)
}

// ──────────────────────────────────────────────
// Database
// ──────────────────────────────────────────────

func initDB() {
	var err error
	db, err = sql.Open("sqlite", config.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS transactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		enrolment_no_date TEXT UNIQUE,
		total_amount_charged REAL,
		gst_amount REAL,
		operator_id TEXT,
		resident_name TEXT,
		upload_date TEXT DEFAULT (date('now'))
	)`)
	if err != nil {
		log.Fatal(err)
	}

	// Migration: add upload_date column if missing (for old databases)
	var colExists bool
	rows, err := db.Query("PRAGMA table_info(transactions)")
	if err == nil {
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull int
			var dflt sql.NullString
			var pk int
			rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			if name == "upload_date" {
				colExists = true
			}
		}
		rows.Close()
	}
	if !colExists {
		_, err = db.Exec("ALTER TABLE transactions ADD COLUMN upload_date TEXT")
		if err != nil {
			log.Println("Migration ALTER error:", err)
		} else {
			db.Exec("UPDATE transactions SET upload_date = date('now') WHERE upload_date IS NULL")
			log.Println("Migration: added upload_date column")
		}
	}
}

func saveTransaction(t Transaction) error {
	_, err := db.Exec(
		`INSERT INTO transactions(enrolment_no_date, total_amount_charged, gst_amount, operator_id, resident_name, upload_date)
		 VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(enrolment_no_date) DO NOTHING`,
		t.EnrolmentNoDate, t.TotalAmountCharged, t.GSTAmount, t.OperatorID, t.ResidentName, t.UploadDate,
	)
	return err
}

func getAnalyticsFiltered(where string) (float64, float64, SlabCounts, int) {
	q := "SELECT total_amount_charged, gst_amount FROM transactions"
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := db.Query(q)
	if err != nil {
		log.Println("Analytics error:", err)
		return 0, 0, SlabCounts{}, 0
	}
	defer rows.Close()
	var rev, gst float64
	var s SlabCounts
	var total int
	for rows.Next() {
		var a, g float64
		rows.Scan(&a, &g)
		rev += a
		gst += g
		total++
		switch {
		case a == 125:
			s.Count125++
		case a == 75:
			s.Count75++
		case a == 0:
			s.Count0++
		}
	}
	return rev, gst, s, total
}

func getAnalytics() (float64, float64, SlabCounts) {
	rev, gst, slabs, _ := getAnalyticsFiltered("")
	return rev, gst, slabs
}

func getHistory(period, fromDate, toDate string) []HistoryRow {
	var groupExpr string
	switch period {
	case "week":
		groupExpr = "strftime('%Y-W%W', upload_date)"
	case "month":
		groupExpr = "strftime('%Y-%m', upload_date)"
	case "year":
		groupExpr = "strftime('%Y', upload_date)"
	default:
		groupExpr = "upload_date"
	}

	where := ""
	if fromDate != "" && toDate != "" {
		where = fmt.Sprintf(" WHERE upload_date >= '%s' AND upload_date <= '%s'", fromDate, toDate)
	} else if fromDate != "" {
		where = fmt.Sprintf(" WHERE upload_date >= '%s'", fromDate)
	} else if toDate != "" {
		where = fmt.Sprintf(" WHERE upload_date <= '%s'", toDate)
	}

	q := fmt.Sprintf(`SELECT %s AS period,
		SUM(total_amount_charged) AS revenue,
		SUM(gst_amount) AS gst,
		SUM(CASE WHEN total_amount_charged = 125 THEN 1 ELSE 0 END) AS c125,
		SUM(CASE WHEN total_amount_charged = 75  THEN 1 ELSE 0 END) AS c75,
		SUM(CASE WHEN total_amount_charged = 0   THEN 1 ELSE 0 END) AS c0,
		COUNT(*) AS total
		FROM transactions%s
		GROUP BY period
		ORDER BY period DESC`, groupExpr, where)

	rows, err := db.Query(q)
	if err != nil {
		log.Println("History error:", err)
		return nil
	}
	defer rows.Close()

	var result []HistoryRow
	for rows.Next() {
		var h HistoryRow
		rows.Scan(&h.Period, &h.Revenue, &h.GST, &h.Count125, &h.Count75, &h.Count0, &h.Total)
		result = append(result, h)
	}
	return result
}

func getDateRange() (string, string) {
	var minDate, maxDate sql.NullString
	db.QueryRow("SELECT MIN(upload_date), MAX(upload_date) FROM transactions").Scan(&minDate, &maxDate)
	return minDate.String, maxDate.String
}

// ──────────────────────────────────────────────
// File Management
// ──────────────────────────────────────────────

func saveToDisk(src io.Reader, filename string) (string, error) {
	dateStr := time.Now().Format("2006-01-02")
	dir := filepath.Join("uploads", dateStr)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dstPath := filepath.Join(dir, filename)
	dst, err := os.Create(dstPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return dstPath, nil
}

// ──────────────────────────────────────────────
// CSV Processing — returns upload summary
// ──────────────────────────────────────────────

func processCSV(r io.Reader) (*UploadResult, error) {
	reader := csv.NewReader(r)
	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}
	hm := make(map[string]int)
	for i, h := range headers {
		hm[strings.TrimSpace(h)] = i
	}
	for _, req := range []string{"ENROLMENT_NO_DATE", "TOTAL_AMOUNT_CHARGED", "GST_AMOUNT", "OPERATOR_ID", "RESIDENT_NAME"} {
		if _, ok := hm[req]; !ok {
			return nil, fmt.Errorf("missing column: %s", req)
		}
	}
	today := time.Now().Format("2006-01-02")
	result := &UploadResult{}
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		amt, _ := strconv.ParseFloat(rec[hm["TOTAL_AMOUNT_CHARGED"]], 64)
		gst, _ := strconv.ParseFloat(rec[hm["GST_AMOUNT"]], 64)
		saveTransaction(Transaction{
			EnrolmentNoDate:    rec[hm["ENROLMENT_NO_DATE"]],
			TotalAmountCharged: amt,
			GSTAmount:          gst,
			OperatorID:         rec[hm["OPERATOR_ID"]],
			ResidentName:       rec[hm["RESIDENT_NAME"]],
			UploadDate:         today,
		})
		result.Rows++
		result.Revenue += amt
		result.GST += gst
		switch {
		case amt == 125:
			result.Slabs.Count125++
		case amt == 75:
			result.Slabs.Count75++
		case amt == 0:
			result.Slabs.Count0++
		}
	}
	return result, nil
}

// ──────────────────────────────────────────────
// Telegram Upload Report
// ──────────────────────────────────────────────

func sendTelegramReport(result *UploadResult) {
	if bot == nil || config.AdminChatID == 0 {
		return
	}
	text := fmt.Sprintf(
		"📥 *CSV Upload Report*\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"📄 File: `%s`\n"+
			"📅 Date: %s\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"📊 *Summary*\n"+
			"   Rows: *%d*\n"+
			"   💰 Revenue: *₹%.2f*\n"+
			"   🧾 GST: *₹%.2f*\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"📈 *Slab Breakdown*\n"+
			"   ₹125: *%d*\n"+
			"   ₹75:  *%d*\n"+
			"   ₹0:   *%d*\n"+
			"━━━━━━━━━━━━━━━━━━━━\n"+
			"✅ Processed successfully",
		result.Filename,
		time.Now().Format("02 Jan 2006, 3:04 PM"),
		result.Rows,
		result.Revenue,
		result.GST,
		result.Slabs.Count125,
		result.Slabs.Count75,
		result.Slabs.Count0,
	)
	msg := tgbotapi.NewMessage(config.AdminChatID, text)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

// ──────────────────────────────────────────────
// Web Handlers
// ──────────────────────────────────────────────

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(embeddedFiles, "templates/index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), 500)
		return
	}
	rev, gst, slabs := getAnalytics()
	tmpl.Execute(w, map[string]interface{}{
		"Revenue": rev, "GST": gst, "Slabs": slabs,
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	file, header, err := r.FormFile("csvfile")
	if err != nil {
		http.Error(w, "Error retrieving file", 400)
		return
	}
	defer file.Close()

	saved, err := saveToDisk(file, header.Filename)
	if err != nil {
		http.Error(w, "Save error: "+err.Error(), 500)
		return
	}
	f, err := os.Open(saved)
	if err != nil {
		http.Error(w, "Open error", 500)
		return
	}
	defer f.Close()
	result, err := processCSV(f)
	if err != nil {
		http.Error(w, "CSV error: "+err.Error(), 500)
		return
	}
	result.Filename = header.Filename

	// Send Telegram report in background
	go sendTelegramReport(result)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	// Only delete the most recent upload batch
	var lastDate sql.NullString
	db.QueryRow("SELECT MAX(upload_date) FROM transactions").Scan(&lastDate)
	if !lastDate.Valid || lastDate.String == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_, err := db.Exec("DELETE FROM transactions WHERE upload_date = ?", lastDate.String)
	if err != nil {
		http.Error(w, "Reset error: "+err.Error(), 500)
		return
	}
	log.Printf("Cleared last upload (%s)", lastDate.String)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "day"
	}
	fromDate := r.URL.Query().Get("from")
	toDate := r.URL.Query().Get("to")

	rows := getHistory(period, fromDate, toDate)
	minDate, maxDate := getDateRange()

	tmpl, err := template.New("history.html").Funcs(template.FuncMap{
		"fmtAmt": func(v float64) string { return fmt.Sprintf("%.2f", v) },
	}).ParseFS(embeddedFiles, "templates/history.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), 500)
		return
	}
	tmpl.Execute(w, map[string]interface{}{
		"Period":  period,
		"Rows":    rows,
		"From":    fromDate,
		"To":      toDate,
		"MinDate": minDate,
		"MaxDate": maxDate,
	})
}

func handleAnalyticsAPI(w http.ResponseWriter, r *http.Request) {
	rev, gst, slabs := getAnalytics()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"revenue": rev, "gst": gst, "slabs": slabs})
}

func handleHistoryAPI(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "day"
	}
	fromDate := r.URL.Query().Get("from")
	toDate := r.URL.Query().Get("to")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getHistory(period, fromDate, toDate))
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(embeddedFiles, "templates/settings.html")
	if err != nil {
		http.Error(w, "Template error", 500)
		return
	}
	tmpl.Execute(w, config)
}

func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	config.TelegramToken = r.FormValue("telegram_token")
	config.AdminChatID, _ = strconv.ParseInt(r.FormValue("admin_chat_id"), 10, 64)
	config.Port = r.FormValue("port")
	config.DBPath = r.FormValue("db_path")
	saveConfigToDisk()
	initBot()
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ──────────────────────────────────────────────
// Telegram Bot
// ──────────────────────────────────────────────

func initBot() {
	if config.TelegramToken == "" {
		return
	}
	var err error
	bot, err = tgbotapi.NewBotAPI(config.TelegramToken)
	if err != nil {
		log.Println("Bot init error:", err)
		bot = nil
		return
	}
	log.Printf("Telegram bot: @%s", bot.Self.UserName)
}

func handleTelegramUpdates() {
	if bot == nil {
		return
	}
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		if config.AdminChatID != 0 && update.Message.Chat.ID != config.AdminChatID {
			bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Unauthorized."))
			continue
		}
		if update.Message.Document != nil {
			go handleTelegramFile(update.Message)
			continue
		}
		if update.Message.IsCommand() {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")
			switch update.Message.Command() {
			case "start":
				msg.Text = "👋 Welcome to *TollVault*!\n\nSend a CSV file or use:\n/status — All-time report\n/today — Today's report\n/backup — Download database"
				msg.ParseMode = "Markdown"
			case "status":
				rev, gst, slabs := getAnalytics()
				msg.Text = fmt.Sprintf(
					"📊 *All‑Time Report*\n"+
						"━━━━━━━━━━━━━━━━━━━━\n"+
						"💰 Revenue: *₹%.2f*\n"+
						"🧾 GST: *₹%.2f*\n"+
						"━━━━━━━━━━━━━━━━━━━━\n"+
						"📈 *Slabs*\n"+
						"   ₹125: *%d*\n"+
						"   ₹75:  *%d*\n"+
						"   ₹0:   *%d*",
					rev, gst, slabs.Count125, slabs.Count75, slabs.Count0)
				msg.ParseMode = "Markdown"
			case "today":
				today := time.Now().Format("2006-01-02")
				rev, gst, slabs, total := getAnalyticsFiltered(fmt.Sprintf("upload_date = '%s'", today))
				msg.Text = fmt.Sprintf(
					"📅 *Today (%s)*\n"+
						"━━━━━━━━━━━━━━━━━━━━\n"+
						"💰 Revenue: *₹%.2f*\n"+
						"🧾 GST: *₹%.2f*\n"+
						"📋 Transactions: *%d*\n"+
						"━━━━━━━━━━━━━━━━━━━━\n"+
						"📈 *Slabs*\n"+
						"   ₹125: *%d*\n"+
						"   ₹75:  *%d*\n"+
						"   ₹0:   *%d*",
					today, rev, gst, total, slabs.Count125, slabs.Count75, slabs.Count0)
				msg.ParseMode = "Markdown"
			case "backup":
				doc := tgbotapi.NewDocument(update.Message.Chat.ID, tgbotapi.FilePath(config.DBPath))
				bot.Send(doc)
				continue
			default:
				msg.Text = "Unknown command. Use /status /today /backup."
			}
			bot.Send(msg)
		}
	}
}

func handleTelegramFile(message *tgbotapi.Message) {
	fi, err := bot.GetFile(tgbotapi.FileConfig{FileID: message.Document.FileID})
	if err != nil {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Error getting file."))
		return
	}
	resp, err := http.Get(fi.Link(config.TelegramToken))
	if err != nil {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Download error."))
		return
	}
	defer resp.Body.Close()

	saved, err := saveToDisk(resp.Body, message.Document.FileName)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Save error: "+err.Error()))
		return
	}
	f, err := os.Open(saved)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Open error."))
		return
	}
	defer f.Close()
	result, err := processCSV(f)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ CSV error: "+err.Error()))
		return
	}
	result.Filename = message.Document.FileName

	// Send detailed report
	sendTelegramReport(result)
}
