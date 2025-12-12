# ghca - GitHub Contributor Analyzer

⚡ High-performance Git repository analyzer to identify vendor vs community contributions

Analyze contributor patterns in any Git repository - track vendor influence, measure community health, and visualize contribution trends over time.

## 🚀 Quick Start

### Installation

Download the latest release for your platform from [Releases](https://github.com/sderosiaux/ghca/releases), or build from source:

```bash
go install github.com/sderosiaux/ghca/cmd/ghca@latest
```

### Basic Usage

```bash
# Analyze a repository
ghca analyze /path/to/repo

# Analyze with vendor classification
ghca analyze /path/to/repo --config vendors.yaml

# Analyze Apache Kafka (full history in ~50 seconds)
ghca analyze /tmp/kafka --config vendors.yaml --workers 16
```

## ⚡ Performance

**40x faster than Python implementations**

- 16,755 commits analyzed in **50 seconds** (Apache Kafka)
- 335 commits/sec with 16 workers
- Concurrent processing with goroutines
- Single binary, zero dependencies

## 📊 Example Output

### Standard Analysis

```
✓ Processed 16,755 commits in 50.5s (335 commits/sec)
✓ Found 1,644 unique contributors

Vendor/Community Breakdown
────────────────────────────────────────────────────────────────────────
Category        Commits   % Commits   Contributors   Lines Added
community        12,471       74.4%          1,501    +2,175,518
confluent         3,890       23.2%            100      +971,436
linkedin            164        1.0%             20       +19,753
aws                 132        0.8%              7       +13,712

🏆 community leads with 74.4% of commits
🌍 Community contributes 74.4% with 1,501 contributors
```

### Timeline Analysis

```bash
ghca analyze /repo --config vendors.yaml --breakdown year
```

```
Period               Total     community           aws     confluent
─────────────────────────────────────────────────────────────────────
2011                   116    116 (100%)             -             -
2015                   761     570 (75%)             -     122 (16%)
2020                 1,405     831 (59%)             -     566 (40%)
2025                 2,138   1,576 (74%)        5 (0%)     557 (26%)

📈 Community: 100.0% → 73.7% ↘
```

## 🎯 Configuration

Create a `vendors.yaml` file to classify contributors by vendor:

```yaml
vendors:
  confluent:
    domains:
      - confluent.io
    github_companies:
      - Confluent
      - Confluent, Inc.
    usernames:
      - jkreps
      - junrao
      - gwenshap

  aws:
    domains:
      - amazon.com
      - aws.amazon.com
    github_companies:
      - Amazon
      - Amazon Web Services
    usernames: []
```

**Classification priority:** username > email domain > GitHub company > community (default)

See [vendors.example.yaml](vendors.example.yaml) for a complete example.

## 📈 Timeline Analysis

Track how vendor contributions evolve over time:

```bash
# Year-over-year trends
ghca analyze /repo --config vendors.yaml --breakdown year

# Quarterly breakdown (since 2023)
ghca analyze /repo --config vendors.yaml --breakdown quarter --since 2023-01-01

# Monthly activity (last 12 months)
ghca analyze /repo --config vendors.yaml --breakdown month --since 2024-01-01

# Weekly changes (recent activity)
ghca analyze /repo --config vendors.yaml --breakdown week --since 2024-11-01
```

## 💡 Use Cases

### Open Source Health Check
Assess community diversity and vendor influence:
```bash
ghca analyze /repo --config vendors.yaml
# Healthy: community > 60%
# Risk: community < 20%
```

### M&A Impact Analysis
Compare contribution patterns before/after acquisitions:
```bash
ghca analyze /repo --until 2024-04-01  # Before
ghca analyze /repo --since 2024-04-01  # After
```

### Trend Analysis
Identify community growth or decline:
```bash
ghca analyze /repo --config vendors.yaml --breakdown year
# Track vendor contributions evolution
# Monitor community engagement trends
```

### Due Diligence
Evaluate vendor lock-in before adopting a technology:
```bash
ghca analyze /repo --config vendors.yaml
# Red flag: single vendor > 80%
# Green flag: diverse contributor base
```

## ⚙️ Command Line Options

```bash
ghca analyze [repo-path] [flags]

Flags:
  -c, --config string      Vendor configuration YAML file
  -b, --breakdown string   Time breakdown: year, quarter, month, week
      --since string       Analyze commits since date (YYYY-MM-DD)
      --until string       Analyze commits until date (YYYY-MM-DD)
  -w, --workers int        Concurrent workers (default: 8)
  -h, --help               Help for analyze
```

### Examples

```bash
# Analyze with 16 workers for maximum speed
ghca analyze /tmp/kafka --workers 16

# Analyze specific time period
ghca analyze /repo --since 2024-01-01 --until 2024-12-31

# Year-over-year breakdown
ghca analyze /repo --config vendors.yaml --breakdown year

# Recent quarterly trends
ghca analyze /repo --config vendors.yaml --breakdown quarter --since 2023-01-01
```

## 🏗️ How It Works

1. **Clone or use local repository** - Works with any Git repository
2. **Configure vendors** (optional) - Define vendor identification rules in YAML
3. **Concurrent analysis** - Processes commits in parallel using goroutines
4. **Classification** - Identifies vendors by username, email domain, or GitHub company
5. **Metrics computation** - Calculates commits, lines changed, and contributor counts
6. **Visualization** - Beautiful terminal output with color-coded results

## 📦 Distribution

Download pre-built binaries from [Releases](https://github.com/sderosiaux/ghca/releases) for:
- Linux (amd64, arm64)
- macOS (Intel, Apple Silicon)
- Windows (amd64)

Or cross-compile yourself:

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o ghca-linux ./cmd/ghca

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o ghca-macos-arm64 ./cmd/ghca

# Windows
GOOS=windows GOARCH=amd64 go build -o ghca.exe ./cmd/ghca
```

## 🔍 What Gets Analyzed

- **Commits**: Total count per vendor/community
- **Contributors**: Unique contributor count
- **Lines Changed**: Additions, deletions, and net changes
- **Timeline**: Evolution over time (year/quarter/month/week)
- **Trends**: Growth or decline in vendor/community participation

## ❓ FAQ

**Q: Do I need GitHub API access?**
A: No! ghca works entirely with local Git repositories, no API calls required.

**Q: How fast is it?**
A: Processes ~335 commits/second on typical hardware with 16 workers. Apache Kafka's 16k commits analyzed in 50 seconds.

**Q: Can I analyze private repositories?**
A: Yes, as long as you have local access to the Git repository.

**Q: What if I don't provide a vendor config?**
A: All contributors will be classified as "community" - still useful for basic statistics.

**Q: Does it modify my repository?**
A: No, ghca is read-only. It never modifies your Git repository.

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

---

Built with ❤️ using Go, [go-git](https://github.com/go-git/go-git), and [lipgloss](https://github.com/charmbracelet/lipgloss)
