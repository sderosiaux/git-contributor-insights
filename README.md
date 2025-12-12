# Git Contributor Insights

⚡ High-performance Git repository analyzer to identify vendor vs community contributions

Analyze contributor patterns in any Git repository - track vendor influence, measure community health, and visualize contribution trends over time.

## 📊 What It Looks Like

### Standard Analysis

```bash
ghca analyze /path/to/kafka
```

```
✓ Processed 16,755 commits in 50.5s (335 commits/sec)
✓ Found 1,644 unique contributors

╭───────────────────────────────────────────╮
│  apache/kafka                              │
│                                           │
│  📊 Total Commits: 16,755                 │
│  👥 Total Contributors: 1,644             │
│  📅 Date Range: 2011-07-25 to 2024-12-11  │
╰───────────────────────────────────────────╯

Vendor/Community Breakdown

Category        Commits   % Commits   Contributors   % Contributors   Lines Added    Lines Deleted
────────────────────────────────────────────────────────────────────────────────────────────────────
community        12,471       74.4%          1,501            91.3%    +2,175,518       -1,456,789
confluent         3,890       23.2%            100             6.1%      +971,436         -623,142
linkedin            164        1.0%             20             1.2%       +19,753          -12,456
aws                 132        0.8%              7             0.4%       +13,712           -8,921
ibm                  98        0.6%             16             1.0%       +11,234           -7,654

Key Insights

🏆 community leads with 74.4% of commits (12,471 commits)
🌍 Community contributes 74.4% of commits with 1,501 contributors
📏 Average commit size: 245 lines changed
```

### Timeline Analysis

```bash
ghca analyze /path/to/kafka --breakdown quarter --since 2024-01-01
```

```
╭───────────────────────────────────────────╮
│  apache/kafka                              │
│                                           │
│  Quarter-over-Quarter                     │
│                                           │
│  📊 Total Periods: 4                      │
│  📅 Date Range: 2024-01-01 to 2024-12-31  │
╰───────────────────────────────────────────╯

Timeline Breakdown

Period               Total     community  confluent   @apache.org        others
─────────────────────────────────────────────────────────────────────────────
2024-Q1                393      99 (25%)   90 (23%)      38 (10%)      166 (42%)
2024-Q2                652     182 (28%)   75 (12%)      66 (10%)      329 (50%)
2024-Q3                633     287 (45%)  107 (17%)      74 (12%)      165 (26%)
2024-Q4                767     376 (49%)  130 (17%)     102 (13%)      159 (21%)


Key Trends

📈 Period Range: 2024-Q1 → 2024-Q4
📊 Commits: 393 → 767 (+374)

🌍 Community: 25.2% → 49.0% ↗
   Contributors: 36 → 52
```

### Automatic Domain Classification

**No config file needed!** Automatically groups contributors by email domain:

```bash
ghca analyze /path/to/kafka
```

```
ℹ No vendor config - using automatic domain classification
  Personal emails (gmail, yahoo, etc.) → 'community'
  Corporate emails → '@domain' (e.g., '@confluent.io', '@amazon.com')

Vendor/Community Breakdown

Category              Commits   % Commits    Contributors
──────────────────────────────────────────────────────────
community                 944       38.6%             113
@confluent.io             402       16.4%              24
@apache.org               280       11.5%              11
@aiven.io                 108        4.4%               8
@apple.com                 29        1.2%               3
others                    682       27.9%             125
```

## 🚀 Quick Start

### Installation

Download the latest release for your platform from [Releases](https://github.com/sderosiaux/git-contributor-insights/releases), or build from source:

```bash
go install github.com/sderosiaux/git-contributor-insights/cmd/ghca@latest
```

### Basic Usage

```bash
# Analyze any repository (automatic domain classification)
ghca analyze /path/to/repo

# Analyze with custom vendor classification
ghca analyze /path/to/repo --config vendors.yaml

# Timeline analysis
ghca analyze /path/to/repo --breakdown quarter --since 2024-01-01

# Faster processing with more workers
ghca analyze /path/to/repo --workers 16
```

## ⚡ Performance

- 16,755 commits analyzed in **50 seconds** (Apache Kafka)
- ~335 commits/sec with 16 workers
- Concurrent processing with goroutines
- Single binary, zero dependencies

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

  aws:
    domains:
      - amazon.com
      - aws.amazon.com
    github_companies:
      - Amazon
      - Amazon Web Services
```

**Classification priority:** email domain > GitHub company > community (default)

**No config needed:** Without a config file, contributors are automatically classified by email domain:
- Personal email providers (gmail, yahoo, outlook, etc.) → `community`
- Corporate domains → `@domain` format (e.g., `@confluent.io`, `@apple.com`)

See [kafka_vendors.yaml](kafka_vendors.yaml) for a complete Apache Kafka example with data streaming companies.

## 📈 Timeline Analysis

Track how vendor contributions evolve over time:

```bash
# Year-over-year trends
ghca analyze /repo --breakdown year

# Quarterly breakdown (since 2023)
ghca analyze /repo --breakdown quarter --since 2023-01-01

# Monthly activity (last 12 months)
ghca analyze /repo --breakdown month --since 2024-01-01

# Weekly changes (recent activity)
ghca analyze /repo --breakdown week --since 2024-11-01
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
ghca analyze /repo --breakdown year
# Track vendor contributions evolution
# Monitor community engagement trends
```

### Due Diligence
Evaluate vendor lock-in before adopting a technology:
```bash
ghca analyze /repo
# Red flag: single vendor > 80%
# Green flag: diverse contributor base
```

## ⚙️ Command Line Options

```bash
ghca analyze [repo-path] [flags]

Flags:
  -c, --config string      Vendor configuration YAML file (optional)
  -b, --breakdown string   Time breakdown: year, quarter, month, week
      --since string       Analyze commits since date (YYYY-MM-DD)
      --until string       Analyze commits until date (YYYY-MM-DD)
  -w, --workers int        Concurrent workers (default: 8)
  -h, --help               Help for analyze
```

### Examples

```bash
# Analyze with automatic domain classification
ghca analyze /tmp/kafka

# Analyze with 16 workers for maximum speed
ghca analyze /tmp/kafka --workers 16

# Analyze specific time period
ghca analyze /repo --since 2024-01-01 --until 2024-12-31

# Year-over-year breakdown with vendor config
ghca analyze /repo --config vendors.yaml --breakdown year

# Recent quarterly trends
ghca analyze /repo --breakdown quarter --since 2023-01-01
```

## 🏗️ How It Works

1. **Clone or use local repository** - Works with any Git repository
2. **Configure vendors** (optional) - Define vendor identification rules in YAML, or use automatic domain classification
3. **Concurrent analysis** - Processes commits in parallel using goroutines
4. **Classification** - Identifies vendors by email domain or GitHub company
5. **Metrics computation** - Calculates commits, lines changed, and contributor counts
6. **Visualization** - Beautiful terminal output with color-coded results

## 📦 Distribution

Download pre-built binaries from [Releases](https://github.com/sderosiaux/git-contributor-insights/releases) for:
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
A: Contributors are automatically classified by email domain - personal emails become "community", corporate domains shown as "@domain".

**Q: Does it modify my repository?**
A: No, the tool is read-only. It never modifies your Git repository.

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

---

Built with ❤️ using Go, [go-git](https://github.com/go-git/go-git), and [lipgloss](https://github.com/charmbracelet/lipgloss)
