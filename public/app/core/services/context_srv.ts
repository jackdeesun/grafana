import { extend } from 'lodash';

import { AnalyticsSettings, OrgRole, rangeUtil, WithAccessControlMetadata } from '@grafana/data';
import { featureEnabled, getBackendSrv } from '@grafana/runtime';
import { getSessionExpiry } from 'app/core/utils/auth';
import { AccessControlAction, UserPermission } from 'app/types';
import { CurrentUserInternal } from 'app/types/config';

import config from '../../core/config';

// When set to auto, the interval will be based on the query range
// NOTE: this is defined here rather than TimeSrv so we avoid circular dependencies
export const AutoRefreshInterval = 'auto';

export class User implements Omit<CurrentUserInternal, 'lightTheme'> {
  isSignedIn: boolean;
  id: number;
  login: string;
  email: string;
  name: string;
  externalUserId: string;
  theme: string;
  orgCount: number;
  orgId: number;
  orgName: string;
  orgRole: OrgRole | '';
  isGrafanaAdmin: boolean;
  gravatarUrl: string;
  timezone: string;
  weekStart: string;
  locale: string;
  language: string;
  helpFlags1: number;
  hasEditPermissionInFolders: boolean;
  permissions?: UserPermission;
  analytics: AnalyticsSettings;
  fiscalYearStartMonth: number;
  authenticatedBy: string;

  constructor() {
    this.id = 0;
    this.isGrafanaAdmin = false;
    this.isSignedIn = false;
    this.orgRole = '';
    this.orgId = 0;
    this.orgName = '';
    this.login = '';
    this.externalUserId = '';
    this.orgCount = 0;
    this.timezone = '';
    this.fiscalYearStartMonth = 0;
    this.helpFlags1 = 0;
    this.theme = 'dark';
    this.hasEditPermissionInFolders = false;
    this.email = '';
    this.name = '';
    this.locale = '';
    this.language = '';
    this.weekStart = '';
    this.gravatarUrl = '';
    this.analytics = {
      identifier: '',
    };
    this.authenticatedBy = '';

    if (config.bootData.user) {
      extend(this, config.bootData.user);
    }
  }
}

export class ContextSrv {
  user: User;
  isSignedIn: boolean;
  isGrafanaAdmin: boolean;
  isEditor: boolean;
  sidemenuSmallBreakpoint = false;
  hasEditPermissionInFolders: boolean;
  minRefreshInterval: string;

  private tokenRotationJobId = 0;

  constructor() {
    if (!config.bootData) {
      config.bootData = { user: {}, settings: {}, navTree: [] } as any;
    }

    this.user = new User();
    this.isSignedIn = this.user.isSignedIn;
    this.isGrafanaAdmin = this.user.isGrafanaAdmin;
    this.isEditor = this.hasRole('Editor') || this.hasRole('Admin');
    this.hasEditPermissionInFolders = this.user.hasEditPermissionInFolders;
    this.minRefreshInterval = config.minRefreshInterval;

    this.scheduleTokenRotationJob();
  }

  async fetchUserPermissions() {
    try {
      if (this.accessControlEnabled()) {
        this.user.permissions = await getBackendSrv().get('/api/access-control/user/actions', {
          reloadcache: true,
        });
      }
    } catch (e) {
      console.error(e);
    }
  }

  /**
   * Indicate the user has been logged out
   */
  setLoggedOut() {
    this.cancelTokenRotationJob();
    this.user.isSignedIn = false;
    this.isSignedIn = false;
    window.location.reload();
  }

  hasRole(role: string) {
    if (role === 'ServerAdmin') {
      return this.isGrafanaAdmin;
    } else {
      return this.user.orgRole === role;
    }
  }

  accessControlEnabled(): boolean {
    return config.rbacEnabled;
  }

  licensedAccessControlEnabled(): boolean {
    return featureEnabled('accesscontrol') && config.rbacEnabled;
  }

  // Checks whether user has required permission
  hasPermissionInMetadata(action: AccessControlAction | string, object: WithAccessControlMetadata): boolean {
    // Fallback if access control disabled
    if (!this.accessControlEnabled()) {
      return true;
    }

    return !!object.accessControl?.[action];
  }

  // Checks whether user has required permission
  hasPermission(action: AccessControlAction | string): boolean {
    // Fallback if access control disabled
    if (!this.accessControlEnabled()) {
      return true;
    }

    return !!this.user.permissions?.[action];
  }

  isGrafanaVisible() {
    return document.visibilityState === undefined || document.visibilityState === 'visible';
  }

  // checks whether the passed interval is longer than the configured minimum refresh rate
  isAllowedInterval(interval: string) {
    if (!config.minRefreshInterval || interval === AutoRefreshInterval) {
      return true;
    }
    return rangeUtil.intervalToMs(interval) >= rangeUtil.intervalToMs(config.minRefreshInterval);
  }

  getValidInterval(interval: string) {
    if (!this.isAllowedInterval(interval)) {
      return config.minRefreshInterval;
    }
    return interval;
  }

  getValidIntervals(intervals: string[]): string[] {
    if (this.minRefreshInterval) {
      return intervals.filter((str) => str !== '').filter(this.isAllowedInterval);
    }
    return intervals;
  }

  hasAccessToExplore() {
    if (this.accessControlEnabled()) {
      return this.hasPermission(AccessControlAction.DataSourcesExplore) && config.exploreEnabled;
    }
    return (this.isEditor || config.viewersCanEdit) && config.exploreEnabled;
  }

  hasAccess(action: string, fallBack: boolean): boolean {
    if (!this.accessControlEnabled()) {
      return fallBack;
    }
    return this.hasPermission(action);
  }

  hasAccessInMetadata(action: string, object: WithAccessControlMetadata, fallBack: boolean): boolean {
    if (!this.accessControlEnabled()) {
      return fallBack;
    }
    return this.hasPermissionInMetadata(action, object);
  }

  // evaluates access control permissions, granting access if the user has any of them; uses fallback if access control is disabled
  evaluatePermission(fallback: () => string[], actions: string[]) {
    if (!this.accessControlEnabled()) {
      return fallback();
    }
    if (actions.some((action) => this.hasPermission(action))) {
      return [];
    }
    // Hack to reject when user does not have permission
    return ['Reject'];
  }

  // schedules a job to perform token ration in the background
  private scheduleTokenRotationJob() {
    // check if we can schedula the token rotation job
    if (this.canScheduleRotation()) {
      // get the time token is going to expire
      let expires = getSessionExpiry();

      // because this job is scheduled for every tab we have open that shares a session we try
      // to distribute the scheduling of the job. For now this can be between 1 and 20 seconds
      const expiresWithDistribution = expires - Math.floor(Math.random() * (20 - 1) + 1);

      // nextRun is when the job should be scheduled for
      let nextRun = expiresWithDistribution * 1000 - Date.now();

      // @ts-ignore
      this.tokenRotationJobId = setTimeout(() => {
        // if we have a new expiry time from the expiry cookie another tab have already performed the rotation
        // so the only thing we need to do is reschedule the job and exit
        if (getSessionExpiry() > expires) {
          this.scheduleTokenRotationJob();
          return;
        }
        this.rotateToken().then();
      }, nextRun);
    }
  }

  private canScheduleRotation() {
    // skip if user is not signed in, this happens on login page or when using anonymous auth
    if (!this.isSignedIn) {
      return false;
    }

    // skip if feature toggle is not enabled
    if (!config.featureToggles.clientTokenRotation) {
      return false;
    }

    // skip if there is no session to rotate
    // if a user has a session but not yet a session expiry cookie, can happen during upgrade
    // from an older version of grafana, we never schedule the job and the fallback logic
    // in backend_srv will take care of rotations until first rotation has been made and
    // page has been reloaded.
    if (getSessionExpiry() === 0) {
      return false;
    }

    return true;
  }

  private cancelTokenRotationJob() {
    if (config.featureToggles.clientTokenRotation && this.tokenRotationJobId > 0) {
      clearTimeout(this.tokenRotationJobId);
    }
  }

  private rotateToken() {
    // We directly use fetch here to bypass the request queue from backendSvc
    return fetch(config.appSubUrl + '/api/user/auth-tokens/rotate', { method: 'POST' })
      .then((res) => {
        if (res.status === 200) {
          this.scheduleTokenRotationJob();
          return;
        }

        if (res.status === 401) {
          this.setLoggedOut();
          return;
        }
      })
      .catch((e) => {
        console.error(e);
      });
  }
}

let contextSrv = new ContextSrv();
export { contextSrv };

export const setContextSrv = (override: ContextSrv) => {
  if (process.env.NODE_ENV !== 'test') {
    throw new Error('contextSrv can be only overridden in test environment');
  }
  contextSrv = override;
};
