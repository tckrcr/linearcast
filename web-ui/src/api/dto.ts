import type { components } from "./generated/schema";

type Schemas = components["schemas"];

export type AdminAuthStatusDTO = Schemas["AdminAuthStatus"];
export type AdminLoginRequestDTO = Schemas["AdminLoginRequest"];
export type AdminPasswordChangeRequestDTO = Schemas["AdminPasswordChangeRequest"];
export type AdminLogoutResponseDTO = Schemas["AdminLogoutResponse"];
export type ErrorResponseDTO = Schemas["ErrorResponse"];
export type AdminNowDTO = Schemas["AdminNow"];
export type ChannelListResponseDTO = Schemas["ChannelListResponse"];
export type CreateChannelRequestDTO = Schemas["CreateChannelRequest"];
export type CreateChannelResponseDTO = Schemas["CreateChannelResponse"];
export type ScheduleBuilderEntryInputDTO = Schemas["ScheduleBuilderEntryInput"];
export type GuideResponseDTO = Schemas["GuideResponse"];
export type PlayableSourcesResponseDTO = Schemas["PlayableSourcesResponse"];
export type SubtitleSettingsDTO = Schemas["SubtitleSettings"];
export type MediaSourceStatusDTO = Schemas["MediaSourceStatus"];
export type PackageProfileListResponseDTO = Schemas["PackageProfileListResponse"];
export type MediaPackageCandidateListDTO = Schemas["MediaPackageCandidateList"];
export type MediaMoviesResponseDTO = Schemas["MediaMoviesResponse"];
export type MediaShowsResponseDTO = Schemas["MediaShowsResponse"];
export type MediaAlbumsResponseDTO = Schemas["MediaAlbumsResponse"];
export type MediaSearchResultDTO = Schemas["MediaSearchResult"];
export type FillerAssetCandidateListDTO = Schemas["FillerAssetCandidateList"];
