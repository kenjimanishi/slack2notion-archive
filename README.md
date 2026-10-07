# slack2notion-archive

SlackのパブリックチャンネルおよびDM（ダイレクトメッセージ）の全履歴を、Notionの「From Slack」ページ配下にある「Slack Archive」データベースへアーカイブするための自作移行ツールです。

Go製の単一バイナリ。Slack/Notionとも公式SDKは使わず、必要なAPI呼び出しのみを薄いクライアントとして自前実装しています。

## 全体の流れ（パイプライン）

Slackから一気にNotionへ流し込まず、各段階の結果を`data/`配下に中間データとして保存します。分類ロジックを直したい場合も、Slackへ再アクセスせず`classify`以降だけ再実行できます。

```
1. export-dm     Slack API(User Token)でDM/グループDMを取得 → data/raw/dm/
2. normalize     公開チャンネル(既存export)+DM取得データを統合、ユーザー名解決、
                 mrkdwn変換、スレッド復元 → data/normalized/
3. classify      チャンネル名を大分類、コンテンツ系統タグをルールベースで付与、
                 Migration ID(冪等性キー)を付与 → data/classified/
4. fetch-files   添付ファイルをダウンロード(閾値内のみ。超過分はSlackリンク保持)
5. schema        Notionに「Slack Archive」データベースを作成(初回のみ)
6. import-notion  data/classified/ の各スレッドをNotionに1ページとして投入
                 (--pilot N で試験投入、Migration IDで重複防止)
7. validate      件数突合・タグ別サンプル抽出レポートを表示
```

## セットアップ

### 1. Slackパブリックチャンネルの公式エクスポート

1. ワークスペースの管理画面（`https://<ワークスペース>.slack.com/admin/settings` → 「インポート/エクスポートデータ」、または https://my.slack.com/services/export ）にアクセスし、エクスポートする期間を指定してリクエスト
   - 標準の自己サービスエクスポートで取得できるのは**パブリックチャンネルのみ**です（プライベートチャンネル・DMは含まれません。プランによらず同様の制限があります。DMは後述のSlack App経由で別途取得します）
2. 完了後、ダウンロードしたzipを展開し、`channels.json` / `users.json` / チャンネルごとのディレクトリが含まれるそのフォルダを本プロジェクト直下に配置（任意の場所に置いた場合は`SLACK_EXPORT_DIR`でパスを指定）

### 2. Slack App の作成（DM取得用）

1. https://api.slack.com/apps で新規App作成
2. 「OAuth & Permissions」→ User Token Scopes に以下を追加:
   `im:history`, `im:read`, `mpim:history`, `mpim:read`, `users:read`, `files:read`
   （`files:read`は`fetch-files`でDMの添付ファイルをダウンロードするために必須。あとからスコープを追加した場合は再インストールしないと反映されません）
3. ワークスペースにインストールし、User OAuth Token (`xoxp-...`) を取得

> 注意: これはSlackのコンプライアンスエクスポート（DM込み全データエクスポート申請。Business+/Enterprise Gridプランで、かつ法的必要性等の承認が前提）とは別物です。ワークスペースオーナー権限があっても、それだけで公式に全DMをエクスポートできるわけではありません。ここで行うのは「自分が参加しているDM/グループDMを、本人の権限でAPI経由取得する」方法です。

### 3. Notion コネクトの作成

1. https://app.notion.com/developers/connections にアクセス（`https://www.notion.so/my-integrations` からもリダイレクトされます）
2. 「新規コネクト」→ コネクト名を入力 → 認証方法は「APIトークン」を選択（今回のような単一ワークスペース向け自作ツールに最適。OAuthはMarketplace配布アプリ向け）→「コネクトを作成」
3. 発行されたAPIトークン（`ntn_...`）を控える（`NOTION_TOKEN`）
4. Notion上に「From Slack」ページを作成（なければ）
5. そのページの「•••」→「コネクト」からこのコネクトを接続
6. ページURLから32文字のページIDを控える（`NOTION_PARENT_PAGE_ID`）

### 4. ビルド

```bash
go build -o bin/migrate ./cmd/migrate
```

## 環境変数

`.env` ファイル（プロジェクト直下）に記述しておくと、CLI実行時に自動で読み込まれます（`.env.example`をコピーして使ってください）。既にシェルの環境変数として設定されている値がある場合はそちらが優先されます。`.env`は`.gitignore`済みのため、誤ってコミットされる心配はありません。

```bash
cp .env.example .env
# .env を編集してトークン等を設定
```

| 変数 | 用途 |
|---|---|
| `SLACK_USER_TOKEN` | `export-dm` に必要（`xoxp-...`）。`fetch-files`でもDM由来ファイルのダウンロード認証に使用（任意だが推奨） |
| `SLACK_WORKSPACE_DOMAIN` | 省略可。`export-dm`を実行しない場合、Slackパーマリンク生成に使用（例: `example.slack.com`） |
| `SLACK_EXPORT_DIR` | 公開チャンネルの公式エクスポートディレクトリ(デフォルト: `./slack_export`) |
| `NOTION_TOKEN` | `schema` / `import-notion` に必要 |
| `NOTION_PARENT_PAGE_ID` | `schema` に必要（「From Slack」ページのID） |
| `NOTION_DATABASE_ID` | `import-notion` に必要（`schema`実行時に出力される） |
| `NOTION_VERSION` | 省略可。Notion-Versionヘッダのオーバーライド(デフォルト `2022-06-28`)。ファイルアップロードAPIがより新しいバージョンを要求する場合に使用 |

## 実行例

```bash
# 1. DM取得（初回のみ。デフォルトは全期間。--since/--untilで期間を絞り込み可能）
export SLACK_USER_TOKEN=xoxp-...
bin/migrate export-dm

# 2. 正規化（公開チャンネル + DM を統合）
bin/migrate normalize

# 3. 分類
bin/migrate classify

# 4. ファイルダウンロード（閾値内のみ。詳細は下記「ファイルの扱い」参照）
bin/migrate fetch-files

# 5. Notionスキーマ作成（初回のみ。出力されたIDをNOTION_DATABASE_IDに設定）
export NOTION_TOKEN=ntn_...
export NOTION_PARENT_PAGE_ID=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
bin/migrate schema

# 6. まずパイロット投入で見た目・分類を確認
export NOTION_DATABASE_ID=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
bin/migrate import-notion --pilot 15

# 7. 問題なければ全件投入（Migration IDで既存ページはスキップされるため、
#    pilotで入れた分も含め安全に再実行できる）
bin/migrate import-notion

# 8. 検証
bin/migrate validate
```

## データ粒度・分類方針

- **1スレッド（返信がなければ1投稿）＝Notionの1ページ**。本文には原文をそのまま時系列・発言者付きで保持し、要約やAI加工は行いません。
- 本文中のSlackの文字装飾（`*太字*` `_斜体_` `~取り消し線~` `` `コード` `` および ` ```コードブロック``` `）とリンク（`<url|表示名>`）は、Notion側でも同等の装飾・クリック可能なリンク・コードブロックとして再現されます。Title（ページタイトル）も同様に装飾を反映します。
- **大分類（Channelプロパティ）＝チャンネル名**（DMは`DM: 相手名`、グループDMは`Group DM: 参加者名,...`）
- **小分類（Tagプロパティ、複数可）＝ルールベースで自動付与**：
  `テキスト` / `画像` / `動画` / `ファイル` / `リンク共有` / `リマインダー` / `システム` / `その他`
  LLMは使用していません。ルールで判定できないものは `その他` にフォールバックするため、`validate`コマンドの出力で後から見直せます。
- 冪等性キー `Migration ID` は `channel_id + thread_ts` のSHA1ハッシュ。再実行時、既存ページはスキップされます。

## ファイルの扱い

- ダウンロード対象は「1ファイル19MB以下（Notionの単一リクエストDirect Upload上限20MiB未満に合わせた安全マージン）」かつ「累計300MB以内」。これを超えるファイルはSlackの元リンク（`permalink`）をNotion上のブックマークとして保持します（Slackのプレサインドリンクのため将来的に失効する可能性があります）。
- ダウンロードしたファイルはNotion Direct Upload APIで再アップロードし、ページ内に埋め込みます。20MBを超えるファイルの分割アップロード（multi-part）には未対応です。

## プロジェクト構成

```
cmd/migrate/          CLIエントリーポイント
internal/model/        パイプライン共通のデータ構造
internal/slackexport/  公式Slackエクスポート(公開チャンネル)の読み込み
internal/slackapi/      Slack Web APIクライアント(DM取得用)
internal/notionapi/     Notion APIクライアント
internal/fileassets/    ファイルダウンロード・閾値判定
internal/pipeline/      各ステージの実装(export-dm/normalize/classify/fetch-files/schema/import-notion/validate)
data/raw/               Slack API(DM)の生データ・workspace.json(gitignore対象)
data/normalized/        正規化後データ(gitignore対象)
data/classified/        分類後データ(gitignore対象)
data/files/             ダウンロードしたファイル本体(gitignore対象)
```

## 既知の制約

- Slackのスレッド再構成は `thread_ts` フィールドに依存します。公開チャンネル側エクスポートにスレッド返信が含まれない場合、各投稿は単独スレッドとして扱われます。
- 1ファイル19MBを超える添付ファイルは（Notionアップロード前の時点で閾値超過として）ダウンロード・再アップロードされず、Slackの元リンクのみ保持されます（将来的に失効する可能性があります）。Notion Direct Upload API自体の上限は20MiBで、これを超えるファイルの分割アップロード（multi-part）には未対応です。
- DM取得は認証ユーザー本人が参加している会話のみが対象です。

## ライセンス

[MIT License](./LICENSE)
